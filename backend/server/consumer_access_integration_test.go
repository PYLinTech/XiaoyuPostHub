//go:build integration

package server

// 分享消费侧权限门禁与配额 fail-closed 的集成测试（真实 PostgreSQL）。
//
// dbtest 会重建 schema 并按序执行 migrations，因此这里同时验证迁移 040 为
// guest 组种下的默认 preview / download；bootstrap 负责"组被移除后重建"的
// 兜底路径。
//
// 运行方式：backend/build.sh --integration-tests（或配置 backend/.test.env）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PYLinTech/XiaoyuPostHub/backend/admin"
	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/bootstrap"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/filestore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/PYLinTech/XiaoyuPostHub/backend/session"
	"github.com/PYLinTech/XiaoyuPostHub/backend/sharing"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
	"github.com/PYLinTech/XiaoyuPostHub/backend/test/dbtest"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

func TestMain(m *testing.M) {
	dbtest.SetupOrExit(m)
	code := m.Run()
	dbtest.Teardown()
	os.Exit(code)
}

// newShareAccessTestServer 用真实仓库构造完整路由。测试只覆盖消费入口
// （权限门禁、配额主体解析），分享/资源在门禁之后才被读取，无需业务数据。
func newShareAccessTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	pool := dbtest.Pool()
	queries := sqlcgen.New(pool)
	groupRepo := group.NewRepo(queries)
	settingsRepo := systemsetting.NewRepo(queries, pool)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>home</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := NewRouter(dir, Deps{
		UserRepo:       user.NewRepo(pool, queries, groupRepo),
		SessionRepo:    session.NewRepo(pool),
		GroupRepo:      groupRepo,
		QuotaRepo:      quota.NewRepo(queries),
		ResourceRepo:   resource.NewRepo(pool, nil),
		SharingRepo:    sharing.NewRepo(pool),
		FileStore:      filestore.New(settingsRepo),
		SystemSettings: settingsRepo,
		// 管理端路由需要 AdminRepo 与 Blobs 非空；本文件的用例只覆盖
		// 用户组权限配置路径，不会调用 Blobs 的方法。
		AdminRepo: admin.NewRepo(pool),
		Blobs:     &blobstore.Service{},
		HTTPS:     true,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return httptest.NewServer(handler)
}

func doAccessRequest(t *testing.T, srv *httptest.Server, method, path string, cookie *http.Cookie) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// setGuestPermissions 重置 guest 组的权限集合，模拟管理员在「权限与配额」
// 中的收紧操作（先清空再按需授予）。
func setGuestPermissions(t *testing.T, codes ...string) {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.Pool()
	if _, err := pool.Exec(ctx, `DELETE FROM group_permissions WHERE group_id=(SELECT id FROM user_groups WHERE name=$1)`, quota.NameGuest); err != nil {
		t.Fatalf("清空 guest 权限: %v", err)
	}
	for _, code := range codes {
		tag, err := pool.Exec(ctx,
			`INSERT INTO group_permissions(group_id,permission) SELECT id,$1 FROM user_groups WHERE name=$2`, code, quota.NameGuest)
		if err != nil {
			t.Fatalf("授予 guest 权限 %s: %v", code, err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("guest 用户组不存在，无法授予 %s", code)
		}
	}
}

// createTestGroup 直接落库创建用户组并授予给定权限（配额方案借用 guest 方案，
// 本文件的用例不触及配额）。
func createTestGroup(t *testing.T, name string, codes ...string) int64 {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.Pool()
	var groupID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO user_groups(name,is_system,description,quota_profile_id,priority)
		SELECT $1,false,'集成测试用户组',qp.id,0 FROM quota_profiles qp WHERE qp.name=$2
		RETURNING id`, name, quota.NameGuest).Scan(&groupID); err != nil {
		t.Fatalf("创建测试用户组 %s: %v", name, err)
	}
	for _, code := range codes {
		if _, err := pool.Exec(ctx,
			`INSERT INTO group_permissions(group_id,permission) VALUES($1,$2)`, groupID, code); err != nil {
			t.Fatalf("授予测试用户组权限 %s: %v", code, err)
		}
	}
	return groupID
}

// createTestUser 直接落库创建账号并加入给定用户组，返回会话 Cookie
// （绕过登录加密通道，专注权限判定）。
func createTestUser(t *testing.T, name string, groupIDs ...int64) *http.Cookie {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.Pool()
	var userID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users(username,password_hash) VALUES($1,'integration-test') RETURNING id`,
		name).Scan(&userID); err != nil {
		t.Fatalf("创建测试用户 %s: %v", name, err)
	}
	for _, groupID := range groupIDs {
		if _, err := pool.Exec(ctx,
			`INSERT INTO user_group_memberships(user_id,group_id) VALUES($1,$2)`, userID, groupID); err != nil {
			t.Fatalf("关联测试用户组: %v", err)
		}
	}
	token, _, err := session.NewRepo(pool).Create(ctx, userID)
	if err != nil {
		t.Fatalf("创建测试会话: %v", err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: token}
}

// createTestUserWithPermissions 创建"独立用户组 + 账号"，返回会话 Cookie。
func createTestUserWithPermissions(t *testing.T, suffix string, codes ...string) *http.Cookie {
	t.Helper()
	groupID := createTestGroup(t, "itg_"+suffix, codes...)
	return createTestUser(t, "itest_"+suffix, groupID)
}

// doAccessJSON 发送带 JSON 请求体的请求（管理端写操作用）。
func doAccessJSON(t *testing.T, srv *httptest.Server, method, path string, payload any, cookie *http.Cookie) (int, string) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(responseBody)
}

// TestGuestGroupDefaultPermissionsAndConsumeGate 覆盖：
//   - 迁移 040 为 guest 组种下默认 preview + download（升级后匿名行为不变）；
//   - 取消 download 后匿名下载与直链 403，取消 preview 后匿名预览 403；
//   - 分享元数据不设消费门禁（页面可打开，动作受控）。
func TestGuestGroupDefaultPermissionsAndConsumeGate(t *testing.T) {
	ctx := context.Background()
	guestPerms, err := group.NewRepo(sqlcgen.New(dbtest.Pool())).PermissionSetByName(ctx, quota.NameGuest)
	if err != nil {
		t.Fatalf("读取 guest 组权限: %v", err)
	}
	for _, code := range []string{permission.Preview, permission.Download} {
		if !guestPerms[code] {
			t.Fatalf("迁移 040 后 guest 组应默认拥有 %s，实际权限=%v", code, guestPerms)
		}
	}

	srv := newShareAccessTestServer(t)
	defer srv.Close()

	// 默认权限：请求穿过门禁，落到"分享不存在"。
	if status, _ := doAccessRequest(t, srv, http.MethodGet, "/api/shares/no-such-token/preview", nil); status != http.StatusNotFound {
		t.Fatalf("默认 guest 权限下预览状态 = %d, want 404（门禁应放行）", status)
	}
	if status, _ := doAccessRequest(t, srv, http.MethodPost, "/api/shares/no-such-token/downloads", nil); status != http.StatusNotFound {
		t.Fatalf("默认 guest 权限下创建下载计划状态 = %d, want 404（门禁应放行）", status)
	}
	if status, _ := doAccessRequest(t, srv, http.MethodGet, "/d/no-such-token", nil); status != http.StatusNotFound {
		t.Fatalf("默认 guest 权限下直链状态 = %d, want 404（门禁应放行）", status)
	}

	// 取消 download：下载计划与直链被拒，预览不受影响。
	setGuestPermissions(t, permission.Preview)
	if status, body := doAccessRequest(t, srv, http.MethodPost, "/api/shares/no-such-token/downloads", nil); status != http.StatusForbidden || !strings.Contains(body, "没有下载权限") {
		t.Fatalf("取消 download 后创建下载计划 = %d/%q, want 403 且提示无下载权限", status, body)
	}
	if status, _ := doAccessRequest(t, srv, http.MethodGet, "/d/no-such-token", nil); status != http.StatusForbidden {
		t.Fatalf("取消 download 后直链状态 = %d, want 403", status)
	}
	if status, _ := doAccessRequest(t, srv, http.MethodGet, "/api/shares/no-such-token/preview", nil); status != http.StatusNotFound {
		t.Fatalf("取消 download 不应影响预览，状态 = %d, want 404", status)
	}

	// 取消 preview：预览被拒；分享元数据仍可读取（页面可打开，动作受控）。
	setGuestPermissions(t)
	if status, body := doAccessRequest(t, srv, http.MethodGet, "/api/shares/no-such-token/preview", nil); status != http.StatusForbidden || !strings.Contains(body, "没有预览权限") {
		t.Fatalf("取消 preview 后预览 = %d/%q, want 403 且提示无预览权限", status, body)
	}
	if status, _ := doAccessRequest(t, srv, http.MethodGet, "/api/shares/no-such-token", nil); status != http.StatusNotFound {
		t.Fatalf("分享元数据不应设消费门禁，状态 = %d, want 404", status)
	}

	// 还原默认，避免影响同包其它用例。
	setGuestPermissions(t, permission.Preview, permission.Download)
}

// TestLoggedInConsumePermissionFollowsOwnGroups 覆盖：登录用户的消费权限
// 只看自身用户组——即便 guest 组（匿名侧）完全放开，没有 download 的账号
// 仍不能下载；有 download 的账号正常穿过门禁。
func TestLoggedInConsumePermissionFollowsOwnGroups(t *testing.T) {
	srv := newShareAccessTestServer(t)
	defer srv.Close()
	setGuestPermissions(t, permission.Preview, permission.Download)

	noDownloadCookie := createTestUserWithPermissions(t, "nodl", permission.Login, permission.Preview)
	if status, body := doAccessRequest(t, srv, http.MethodPost, "/api/shares/no-such-token/downloads", noDownloadCookie); status != http.StatusForbidden || !strings.Contains(body, "没有下载权限") {
		t.Fatalf("无 download 账号创建下载计划 = %d/%q, want 403", status, body)
	}
	if status, _ := doAccessRequest(t, srv, http.MethodGet, "/d/no-such-token", noDownloadCookie); status != http.StatusForbidden {
		t.Fatalf("无 download 账号访问直链 = %d, want 403", status)
	}
	// 同一账号拥有 preview：预览穿过门禁，落到"分享不存在"。
	if status, _ := doAccessRequest(t, srv, http.MethodGet, "/api/shares/no-such-token/preview", noDownloadCookie); status != http.StatusNotFound {
		t.Fatalf("有 preview 账号预览状态 = %d, want 404（门禁应放行）", status)
	}

	downloadCookie := createTestUserWithPermissions(t, "withdl", permission.Login, permission.Download)
	if status, _ := doAccessRequest(t, srv, http.MethodPost, "/api/shares/no-such-token/downloads", downloadCookie); status != http.StatusNotFound {
		t.Fatalf("有 download 账号创建下载计划 = %d, want 404（门禁应放行）", status)
	}
}

// TestGuestConfigMissingFailsClosed 覆盖 fail-closed：
//   - guest 用户组被删除后，匿名预览 / 下载 / 直链一律 503，不再静默放行；
//   - 登录用户按自身身份解析，不受 guest 配置缺失影响；
//   - bootstrap 幂等重建 guest 组并补齐默认权限后服务恢复。
func TestGuestConfigMissingFailsClosed(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool()
	srv := newShareAccessTestServer(t)
	defer srv.Close()

	if _, err := pool.Exec(ctx, `DELETE FROM user_groups WHERE name=$1`, quota.NameGuest); err != nil {
		t.Fatalf("删除 guest 用户组: %v", err)
	}

	if status, _ := doAccessRequest(t, srv, http.MethodGet, "/api/shares/no-such-token/preview", nil); status != http.StatusServiceUnavailable {
		t.Fatalf("guest 组缺失时匿名预览 = %d, want 503（fail-closed）", status)
	}
	if status, _ := doAccessRequest(t, srv, http.MethodPost, "/api/shares/no-such-token/downloads", nil); status != http.StatusServiceUnavailable {
		t.Fatalf("guest 组缺失时匿名下载 = %d, want 503（fail-closed）", status)
	}
	if status, _ := doAccessRequest(t, srv, http.MethodGet, "/d/no-such-token", nil); status != http.StatusServiceUnavailable {
		t.Fatalf("guest 组缺失时匿名直链 = %d, want 503（fail-closed）", status)
	}

	// 登录用户不受 guest 配置缺失影响：按自身用户组判定。
	loggedInCookie := createTestUserWithPermissions(t, "guestless", permission.Login, permission.Download)
	if status, _ := doAccessRequest(t, srv, http.MethodPost, "/api/shares/no-such-token/downloads", loggedInCookie); status != http.StatusNotFound {
		t.Fatalf("guest 组缺失时登录用户下载 = %d, want 404（按自身权限放行）", status)
	}

	// 启动期 bootstrap 兜底：重建 guest 组并补种默认权限，匿名服务恢复。
	if err := bootstrap.NewAuthCatalog(pool).Run(ctx); err != nil {
		t.Fatalf("bootstrap 重建 guest 组: %v", err)
	}
	perms, err := group.NewRepo(sqlcgen.New(pool)).PermissionSetByName(ctx, quota.NameGuest)
	if err != nil {
		t.Fatalf("重建后读取 guest 组权限: %v", err)
	}
	for _, code := range []string{permission.Preview, permission.Download} {
		if !perms[code] {
			t.Fatalf("bootstrap 重建的 guest 组应默认拥有 %s，实际权限=%v", code, perms)
		}
	}
	if status, _ := doAccessRequest(t, srv, http.MethodGet, "/api/shares/no-such-token/preview", nil); status != http.StatusNotFound {
		t.Fatalf("恢复后匿名预览 = %d, want 404（门禁应放行）", status)
	}
}

// TestGroupAdminActionsRequireActorCoverage 覆盖 P2：非超级管理员只能维护
// "权限不超过自身"的用户组——权限配置、改名、删除都不允许作用于高权限组，
// 否则持有 ManagePermissions / ManageUserGroups 的管理员可把高权组降权
// （清空 default_user 的管理权限、关闭 guest 的匿名预览/下载）。
func TestGroupAdminActionsRequireActorCoverage(t *testing.T) {
	ctx := context.Background()
	srv := newShareAccessTestServer(t)
	defer srv.Close()

	// 操作者：只持 login + manage_permissions + manage_user_groups。
	ownGroupID := createTestGroup(t, "itg_permadmin",
		permission.Login, permission.ManagePermissions, permission.ManageUserGroups)
	actorCookie := createTestUser(t, "itest_permadmin", ownGroupID)
	// 高权组：含操作者没有的 manage_system。
	highGroupID := createTestGroup(t, "itg_highperm", permission.Login, permission.ManageSystem)

	var guestGroupID int64
	if err := dbtest.Pool().QueryRow(ctx, `SELECT id FROM user_groups WHERE name=$1`, quota.NameGuest).Scan(&guestGroupID); err != nil {
		t.Fatalf("读取 guest 用户组: %v", err)
	}

	// 1) 权限配置：不能摘掉高权组的 manage_system。
	if status, body := doAccessJSON(t, srv, http.MethodPut,
		fmt.Sprintf("/api/admin/access/groups/%d/permissions", highGroupID),
		map[string]any{"permissions": []string{permission.Login}}, actorCookie); status != http.StatusForbidden {
		t.Fatalf("低权管理员改高权组权限 = %d/%q, want 403", status, body)
	}
	// 2) 权限配置：不能清空 guest 组的匿名预览/下载权限。
	if status, body := doAccessJSON(t, srv, http.MethodPut,
		fmt.Sprintf("/api/admin/access/groups/%d/permissions", guestGroupID),
		map[string]any{"permissions": []string{}}, actorCookie); status != http.StatusForbidden {
		t.Fatalf("低权管理员清空 guest 权限 = %d/%q, want 403", status, body)
	}
	// 3) 改名高权组：拒绝。
	if status, body := doAccessJSON(t, srv, http.MethodPut,
		fmt.Sprintf("/api/admin/user-groups/%d", highGroupID),
		map[string]any{"name": "itg_highperm2", "description": "renamed"}, actorCookie); status != http.StatusForbidden {
		t.Fatalf("低权管理员改名高权组 = %d/%q, want 403", status, body)
	}
	// 4) 删除高权组：拒绝。
	if status, body := doAccessRequest(t, srv, http.MethodDelete,
		fmt.Sprintf("/api/admin/user-groups/%d", highGroupID), actorCookie); status != http.StatusForbidden {
		t.Fatalf("低权管理员删除高权组 = %d/%q, want 403", status, body)
	}
	// 5) 覆盖范围内的组（操作者自身所在的组）：允许保存。
	if status, body := doAccessJSON(t, srv, http.MethodPut,
		fmt.Sprintf("/api/admin/access/groups/%d/permissions", ownGroupID),
		map[string]any{"permissions": []string{permission.Login, permission.ManagePermissions, permission.ManageUserGroups}},
		actorCookie); status != http.StatusOK {
		t.Fatalf("低权管理员配置自身所在组 = %d/%q, want 200", status, body)
	}

	// 高权组权限未被任何被拒操作改动。
	rows, err := dbtest.Pool().Query(ctx,
		`SELECT permission FROM group_permissions WHERE group_id=$1 ORDER BY permission`, highGroupID)
	if err != nil {
		t.Fatalf("读取高权组权限: %v", err)
	}
	defer rows.Close()
	perms := make([]string, 0, 2)
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			t.Fatal(err)
		}
		perms = append(perms, code)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(perms) != 2 || perms[0] != permission.Login || perms[1] != permission.ManageSystem {
		t.Fatalf("高权组权限被改动：%v", perms)
	}
}
