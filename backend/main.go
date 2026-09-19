// Package main 启动 XiaoyuPostHub 后端：监听 :8080，提供 API 与前端静态文件。
//
// 启动流程：
//  1. 加载 ENV_FILE 指定的配置文件（默认 deploy/.env）
//  2. 加载与校验配置（config.Load）
//  3. 连接 PostgreSQL（db.Open，启动期 Ping 一次）
//  4. 初始化 system_settings 默认行（不覆盖已有配置）
//  5. BootstrapAuthCatalog（默认配额方案和默认用户组）
//  6. BootstrapSuperAdmin（创建/同步超管账号并绑定默认用户组）
//  7. 构造 group / quota / user 等仓库
//  8. 启动 HTTP server（注入 repo）
//  9. 收到 SIGINT/SIGTERM 优雅关闭
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/admin"
	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/bootstrap"
	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/filestore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/inbox"
	"github.com/PYLinTech/XiaoyuPostHub/backend/loginseal"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/PYLinTech/XiaoyuPostHub/backend/server"
	"github.com/PYLinTech/XiaoyuPostHub/backend/session"
	"github.com/PYLinTech/XiaoyuPostHub/backend/sharing"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
	"github.com/PYLinTech/XiaoyuPostHub/backend/upload"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

func main() {
	if os.Getenv("XPH_INTERNAL_HASH_PASSWORD") == "true" {
		password, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
		if err != nil || len(password) == 0 || len(password) > 4096 {
			log.Fatal("invalid password input")
		}
		// 管理员密码与站内新设密码适用同一套规则（8 至 18 位、至少两类字符），
		// 校验失败时安装脚本会提示重新输入。
		if err := user.ValidateNewPassword(string(password)); err != nil {
			log.Fatal(err)
		}
		hash, err := user.HashPassword(string(password))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(hash)
		return
	}
	if action := os.Getenv("XPH_INTERNAL_DATABASE_ACTION"); action != "" {
		runInternalDatabaseAction(action)
		return
	}

	envFile := defaultEnvFile()
	cfg, err := config.Load(envFile)
	if err != nil {
		log.Fatalf("配置加载失败：%v", err)
	}
	log.Printf("配置加载成功：ENV_FILE=%s", displayEnvFile(envFile))

	config.EnvSuperAdmin = cfg.SuperAdminUsername
	config.EnvSuperAdminPasswordHash = cfg.SuperAdminPasswordHash

	bootCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	database, err := db.Open(bootCtx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		log.Fatalf("数据库初始化失败：%v", err)
	}
	log.Printf("数据库已连接：%s", db.DescribeURL(cfg.DatabaseURL))

	q := sqlcgen.New(database.Pool())

	// 1. 初始化程序自身的非敏感运行期配置；已有值不会被默认值覆盖。
	settingsCtx, settingsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	settingsRepo := systemsetting.NewRepo(q, database.Pool())
	if err := settingsRepo.EnsureDefaults(settingsCtx); err != nil {
		settingsCancel()
		log.Fatalf("初始化系统配置失败：%v", err)
	}
	settingsCancel()
	// 2. BootstrapAuthCatalog：默认配额方案和默认用户组
	bootCtx2, bootCancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	if err := bootstrap.NewAuthCatalog(database.Pool()).Run(bootCtx2); err != nil {
		bootCancel2()
		log.Fatalf("BootstrapAuthCatalog 失败：%v", err)
	}
	bootCancel2()

	// 4. BootstrapSuperAdmin：固定绑定 default_user，确保权限和配额都有组来源
	bootCtx3, bootCancel3 := context.WithTimeout(context.Background(), 5*time.Second)
	if err := user.BootstrapSuperAdmin(bootCtx3, database.Pool()); err != nil {
		bootCancel3()
		log.Fatalf("初始化超管失败：%v", err)
	}
	bootCancel3()

	// 5. 构造物理对象服务（本机后端根目录来自 system_settings.storage_path）
	blobRoot := func(ctx context.Context) (string, error) {
		settings, err := settingsRepo.Get(ctx)
		if err != nil {
			return "", err
		}
		root := filepath.Clean(settings.StoragePath)
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("存储路径必须是绝对路径：%s", root)
		}
		return root, nil
	}
	encryptionConfig, err := blobstore.ParseEncryptionKeys(cfg.EncryptionKeys)
	if err != nil {
		log.Fatalf("解析加密密钥配置失败：%v", err)
	}
	if encryptionConfig.Available() {
		log.Printf("文件加密密钥已加载：主密钥 keyId=%s（可用密钥 %d 个）",
			encryptionConfig.PrimaryKeyID(), encryptionConfig.KeyCount())
	}
	blobService := blobstore.NewService(database.Pool(), blobRoot, blobstore.ServiceOptions{
		Encryption: encryptionConfig,
		Pan123: blobstore.Pan123Credentials{
			ClientID:     cfg.Pan123ClientID,
			ClientSecret: cfg.Pan123ClientSecret,
		},
		S3: blobstore.S3Credentials{
			AccessKeyID:     cfg.S3AccessKeyID,
			SecretAccessKey: cfg.S3SecretAccessKey,
		},
	})
	blobReloadCtx, blobReloadCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := blobService.Reload(blobReloadCtx); err != nil {
		blobReloadCancel()
		log.Fatalf("初始化存储后端失败：%v", err)
	}
	blobReloadCancel()
	if initialSettings, settingsErr := settingsRepo.Get(context.Background()); settingsErr == nil &&
		initialSettings.EncryptNewFiles && !encryptionConfig.Available() {
		log.Printf("警告：已开启新文件加密，但未配置 XPH_ENCRYPTION_KEYS，上传新文件将失败")
	}

	// 6. 构造 Repo
	groupRepo := group.NewRepo(q)
	quotaRepo := quota.NewRepo(q)
	userRepo := user.NewRepo(database.Pool(), q, groupRepo)
	sessionRepo := session.NewRepo(database.Pool())
	resourceRepo := resource.NewRepo(database.Pool(), blobService)
	sharingRepo := sharing.NewRepo(database.Pool())
	fileStore := filestore.New(settingsRepo)
	adminRepo := admin.NewRepo(database.Pool())
	inboxRepo := inbox.NewRepo(database.Pool())
	uploadRepo := upload.NewRepo(database.Pool())
	uploadRecoveryCtx, uploadRecoveryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := uploadRepo.RecoverInterrupted(uploadRecoveryCtx); err != nil {
		uploadRecoveryCancel()
		log.Fatalf("恢复上传队列失败：%v", err)
	}
	uploadRecoveryCancel()
	staticPath := cfg.StaticDir

	// 登录载荷加密：密钥对只在内存中生成，进程重启即轮换，不落盘、不入库；
	// 允许的填充方式由 HTTPS_ENABLED 固定，HTTPS 部署不接受弱填充。
	passwordSeal, err := loginseal.New(cfg.HTTPSEnabled)
	if err != nil {
		log.Fatalf("初始化登录加密密钥失败：%v", err)
	}
	log.Printf("登录载荷加密已启用：keyId=%s，算法=%s（内存密钥，重启自动轮换）", passwordSeal.KeyID(), passwordSeal.Algorithm())

	// 注入可信反向代理网段：仅命中网段的直连来源才会采信 X-Real-IP（防伪造）。
	server.SetTrustedProxies(cfg.TrustedProxyCIDRs)
	if len(cfg.TrustedProxyCIDRs) == 0 {
		log.Printf("未配置 TRUSTED_PROXY_CIDRS：不采信 X-Real-IP（直连部署为正确设置；若部署在反向代理之后，请配置该变量，否则限流将按代理 IP 统计）")
	}

	handler, err := server.NewRouter(staticPath, server.Deps{
		UserRepo:       userRepo,
		SessionRepo:    sessionRepo,
		GroupRepo:      groupRepo,
		QuotaRepo:      quotaRepo,
		ResourceRepo:   resourceRepo,
		SharingRepo:    sharingRepo,
		FileStore:      fileStore,
		HostDiskPath:   cfg.HostDiskPath,
		SystemSettings: settingsRepo,
		AdminRepo:      adminRepo,
		InboxRepo:      inboxRepo,
		UploadRepo:     uploadRepo,
		Blobs:          blobService,
		HTTPS:          cfg.HTTPSEnabled,
		PasswordSeal:   passwordSeal,
		HSTSEnabled:    cfg.HSTSEnabled,
	})
	if err != nil {
		log.Fatalf("初始化 HTTP 路由失败：%v", err)
	}

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// 文件传输由请求体大小、配额和校验约束；不使用全局短超时截断大文件。
		ReadTimeout:  0,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	cleanupCtx, stopCleanup := context.WithCancel(context.Background())
	defer stopCleanup()
	go sessionRepo.StartCleanup(cleanupCtx)
	go sharingRepo.StartDownloadJobCleanup(cleanupCtx)
	// 存储维护任务执行器（跨后端迁移 / 补加密 / 孤儿清理）。
	go server.RunStorageTasks(cleanupCtx, adminRepo, blobService)
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-cleanupCtx.Done():
				return
			case <-ticker.C:
				ids, err := uploadRepo.DeleteExpired(cleanupCtx)
				if err != nil {
					log.Printf("清理过期上传任务失败：%v", err)
				} else {
					for _, id := range ids {
						if err := fileStore.RemoveUploadSession(cleanupCtx, id); err != nil {
							log.Printf("清理上传分片目录失败 id=%s: %v", id, err)
						}
					}
				}
				// 已完成上传会话的元数据按保留期回收：完成路径保留 DB 行是为了
				// 幂等重放，但会话/分片行会随每次上传无界增长，需定期清理。
				if removed, err := uploadRepo.DeleteCompletedBefore(cleanupCtx, time.Now().Add(-7*24*time.Hour)); err != nil {
					log.Printf("清理已完成上传任务记录失败：%v", err)
				} else if removed > 0 {
					log.Printf("已清理 %d 条已完成上传任务记录", removed)
				}
				settings, err := settingsRepo.Get(cleanupCtx)
				if err != nil {
					log.Printf("读取回收期限失败：%v", err)
					continue
				}
				before := time.Now().Add(-time.Duration(settings.TrashRetentionDays) * 24 * time.Hour)
				// 到期只标记"用户不可见、不可恢复"：资源行与物理文件保留，
				// 管理员审查页与存储清理仍能看到，物理删除由管理员执行。
				marked, err := resourceRepo.MarkTrashExpiredPurged(cleanupCtx, before)
				if err != nil {
					log.Printf("处理过期回收站失败：%v", err)
					continue
				}
				if marked > 0 {
					log.Printf("已将 %d 项超过回收期限的内容标记为彻底删除（物理文件保留待管理员清理）", marked)
				}
				// 孤儿对象不做任何自动清理：物理删除只由管理员在「存储管理 →
				// 维护任务 → 孤立文件」手动发起（先扫描出清单、确认后再清理）。
				// 后台循环只标记"用户不可见"，绝不删除物理文件。
				// 取件码维护：释放已失效（停用/过期/删除）取件码的占位，使码空间可
				// 被重新分配。永久取件码长期积累会挤占码空间，这里自动腾位置。
				if freed, err := sharingRepo.ReleaseDeadPickupCodes(cleanupCtx, nil); err != nil {
					log.Printf("释放失效取件码失败：%v", err)
				} else if freed > 0 {
					log.Printf("已释放 %d 个失效取件码（码空间可重新分配）", freed)
				}
			}
		}
	}()
	go func() {
		log.Printf("XiaoyuPostHub 后端已启动，监听 %s，静态目录：%s", srv.Addr, staticPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		log.Printf("收到信号 %s，开始优雅关闭", sig)
	case err := <-errCh:
		log.Fatalf("服务异常：%v", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server 优雅关闭异常：%v", err)
	}

	database.Close()
	log.Printf("数据库连接池已关闭，后端退出")
}

func runInternalDatabaseAction(action string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	switch action {
	case "test":
		if err := db.TestInstallConnection(ctx, os.Getenv("DATABASE_URL")); err != nil {
			log.Fatal(err)
		}
	case "provision":
		passwordBytes := make([]byte, 24)
		if _, err := rand.Read(passwordBytes); err != nil {
			log.Fatal("生成数据库密码失败")
		}
		password := base64.RawURLEncoding.EncodeToString(passwordBytes)
		databaseURL, err := db.ProvisionDatabase(
			ctx,
			os.Getenv("XPH_DATABASE_ADMIN_URL"),
			"xiaoyuposthub",
			"xiaoyuposthub",
			password,
		)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(databaseURL)
	default:
		log.Fatal("不支持的数据库安装操作")
	}
}

func defaultEnvFile() string {
	if v := os.Getenv("ENV_FILE"); v != "" {
		return v
	}
	return "deploy/.env"
}

func displayEnvFile(p string) string {
	if p == "" {
		return "<none, only env vars>"
	}
	return p
}
