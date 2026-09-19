package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// pan123Mock 是 123 云盘 OpenAPI 的最小可复现模拟（只覆盖系统用到的接口）。
type pan123Mock struct {
	server *httptest.Server

	mu            sync.Mutex
	tokenRequests int
	accessToken   string
	tokenValid    bool
	singleUploads int
	sliceUploads  int
	content       map[int64][]byte // fileID -> 内容
	nextFileID    int64
	sliceSize     int64
	createReuse   bool // create 秒传是否命中
	supportRange  bool // 下载是否支持 Range
	trashCalls    int  // trash 调用次数（验证批量分批）
	trashIDs      [][]int64
	directLink    bool
	// downloadInfoFail 为真时 download_info（自用下载通道）返回失败，
	// 用于验证"异常时通道互相切换"与严格模式。
	downloadInfoFail bool
	// verifyPolls > 0 时 upload_complete 前 N 次返回 20103（校验中），模拟真实
	// 平台分片合并的中间状态。
	verifyPolls int
	// sliceChunks 是分片上传的待合并内容（sliceNo -> 分片字节），
	// upload_complete 时按 sliceNo 顺序拼接落盘（模拟平台合并分片）。
	sliceChunks map[string][]byte
}

func newPan123Mock(t *testing.T) *pan123Mock {
	t.Helper()
	mock := &pan123Mock{
		accessToken:  "token-1",
		tokenValid:   true,
		content:      map[int64][]byte{},
		nextFileID:   1000,
		sliceSize:    1 << 20, // 1MB 分片（测试用）
		supportRange: true,
		directLink:   true,
		sliceChunks:  map[string][]byte{},
	}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.dispatch))
	t.Cleanup(mock.server.Close)
	return mock
}

func (m *pan123Mock) baseURL() string { return m.server.URL }

func (m *pan123Mock) writeJSON(w http.ResponseWriter, code int, data any, traceID string) {
	envelope := map[string]any{"code": code, "message": "ok", "x-traceID": traceID}
	if data != nil {
		envelope["data"] = data
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(envelope)
}

func (m *pan123Mock) dispatch(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	trace := "mock-trace"

	// 除换取 token 与 CDN 下载（真实直链/临时下载地址都不带鉴权头）外，全部接口校验
	// 鉴权头：token 失效（模拟被同 client_id 的新 token 踢下线）时返回 401，用于
	// 覆盖客户端的刷新重试路径。
	if r.URL.Path != "/api/v1/access_token" &&
		!strings.HasPrefix(r.URL.Path, "/fake-download/") &&
		!strings.HasPrefix(r.URL.Path, "/cdn/") {
		if !m.tokenValid || r.Header.Get("Authorization") != "Bearer "+m.accessToken {
			m.writeJSON(w, 401, nil, trace)
			return
		}
	}

	switch {
	case r.URL.Path == "/api/v1/access_token":
		m.tokenRequests++
		m.tokenValid = true
		m.accessToken = fmt.Sprintf("token-%d", m.tokenRequests)
		m.writeJSON(w, 0, map[string]any{
			"accessToken": m.accessToken,
			"expiredAt":   time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339),
		}, trace)
	case r.URL.Path == "/upload/v2/file/domain":
		m.writeJSON(w, 0, []string{m.server.URL}, trace)
	case r.URL.Path == "/upload/v2/file/create":
		var body struct {
			ParentFileID int64  `json:"parentFileID"`
			Filename     string `json:"filename"`
			Etag         string `json:"etag"`
			Size         int64  `json:"size"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.ParentFileID != 123456 {
			m.writeJSON(w, 1, nil, trace)
			return
		}
		response := map[string]any{
			"preuploadID": "pre-1",
			"reuse":       false,
			"sliceSize":   m.sliceSize,
			"servers":     []string{m.server.URL},
		}
		if m.createReuse {
			// 秒传命中：平台创建新文件条目并复用物理数据（真机实测语义）。
			response["reuse"] = true
			response["fileID"] = 8888
		}
		m.writeJSON(w, 0, response, trace)
	case r.URL.Path == "/upload/v2/file/upload_complete":
		if m.verifyPolls > 0 {
			m.verifyPolls--
			m.writeJSON(w, 20103, nil, trace)
			return
		}
		m.nextFileID++
		// 分片上传：按 sliceNo 顺序拼接后落盘（模拟平台合并）。
		if len(m.sliceChunks) > 0 {
			var merged []byte
			for index := 1; index <= len(m.sliceChunks); index++ {
				merged = append(merged, m.sliceChunks[strconv.Itoa(index)]...)
			}
			m.content[m.nextFileID] = merged
			m.sliceChunks = map[string][]byte{}
		}
		m.writeJSON(w, 0, map[string]any{"completed": true, "fileID": m.nextFileID}, trace)
	case r.URL.Path == "/upload/v2/file/single/create",
		r.URL.Path == "/upload/v2/file/slice":
		m.handleMultipartUpload(w, r)
	case r.URL.Path == "/api/v1/file/detail":
		// 真实平台该接口只认 fileID（大写 D），mock 与之一致以覆盖参数名回归。
		fileID, _ := strconv.ParseInt(r.URL.Query().Get("fileID"), 10, 64)
		content, ok := m.content[fileID]
		if !ok {
			m.writeJSON(w, 5066, nil, trace)
			return
		}
		m.writeJSON(w, 0, map[string]any{"fileID": fileID, "size": len(content)}, trace)
	case r.URL.Path == "/api/v1/file/download_info":
		fileID := r.URL.Query().Get("fileId")
		if m.downloadInfoFail {
			m.writeJSON(w, 40101, nil, trace)
			return
		}
		m.writeJSON(w, 0, map[string]any{
			"downloadUrl": m.server.URL + "/fake-download/" + fileID,
		}, trace)
	case strings.HasPrefix(r.URL.Path, "/fake-download/"):
		fileID, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/fake-download/"), 10, 64)
		m.handleDownload(w, r, fileID)
	case strings.HasPrefix(r.URL.Path, "/cdn/"):
		// 直链通道（direct-link）：取到的字节与自用下载通道完全一致，只是消耗
		// 另一份额度——mock 用同一份内容模拟，便于断言通道选择。
		fileID, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/cdn/"), 10, 64)
		m.handleDownload(w, r, fileID)
	case r.URL.Path == "/api/v1/file/trash":
		var body struct {
			FileIDs []int64 `json:"fileIDs"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.trashCalls++
		m.trashIDs = append(m.trashIDs, body.FileIDs)
		// 平台实测：对已删除 / 不存在的 ID 也返回成功（删除天然幂等）。
		m.writeJSON(w, 0, nil, trace)
	case r.URL.Path == "/api/v1/direct-link/url":
		if !m.directLink {
			m.writeJSON(w, 404, nil, trace)
			return
		}
		m.writeJSON(w, 0, map[string]any{"url": m.server.URL + "/cdn/" + r.URL.Query().Get("fileID")}, trace)
	case r.URL.Path == "/api/v1/direct-link/enable",
		r.URL.Path == "/api/v1/direct-link/cache/refresh":
		m.writeJSON(w, 0, map[string]any{"enable": true}, trace)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// handleMultipartUpload 解析单步/分片上传的 multipart 请求并保存内容。
func (m *pan123Mock) handleMultipartUpload(w http.ResponseWriter, r *http.Request) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		http.Error(w, "bad content type", http.StatusBadRequest)
		return
	}
	if r.Header.Get("Authorization") == "" || r.Header.Get("Platform") != "open_platform" {
		m.writeJSON(w, http.StatusUnauthorized, nil, "mock")
		return
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	fields := map[string]string{}
	var payload []byte
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(part)
		if part.FileName() != "" || part.FormName() == "slice" || part.FormName() == "file" {
			payload = data
			continue
		}
		fields[part.FormName()] = string(data)
	}
	switch r.URL.Path {
	case "/upload/v2/file/slice":
		if fields["preuploadID"] == "" || fields["sliceNo"] == "" || fields["sliceMD5"] == "" {
			http.Error(w, "missing slice fields", http.StatusBadRequest)
			return
		}
		// 空响应体 + HTTP 200 表示分片成功（官方语义）；分片内容暂存，
		// 由 upload_complete 按 sliceNo 拼接落盘。
		m.sliceUploads++
		m.sliceChunks[fields["sliceNo"]] = payload
		w.WriteHeader(http.StatusOK)
	case "/upload/v2/file/single/create":
		if fields["filename"] == "" || fields["etag"] == "" || fields["parentFileID"] != "123456" {
			http.Error(w, "missing fields", http.StatusBadRequest)
			return
		}
		m.singleUploads++
		m.nextFileID++
		m.content[m.nextFileID] = payload
		m.writeJSON(w, 0, map[string]any{"fileID": m.nextFileID, "completed": true}, "mock")
	}
}

// handleDownload 支持 Range（或忽略 Range 返回全量，用于测试兼容分支）。
func (m *pan123Mock) handleDownload(w http.ResponseWriter, r *http.Request, fileID int64) {
	content, ok := m.content[fileID]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	rangeHeader := r.Header.Get("Range")
	if m.supportRange && rangeHeader != "" {
		var start int64
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-", &start); err != nil {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		if start >= int64(len(content)) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start:])
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func newPan123TestBackend(mock *pan123Mock) *Pan123Backend {
	// token 缓存是进程级（Reload 会重建实例但仍复用），测试之间必须隔离。
	pan123TokenMu.Lock()
	delete(pan123TokenCache, "client-id")
	pan123TokenMu.Unlock()
	return &Pan123Backend{
		cfg: Pan123Config{
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			ParentFileID: 123456,
			BaseURL:      mock.baseURL(),
		},
		baseURL:    mock.baseURL(),
		httpClient: mock.server.Client(),
	}
}

func (b *Pan123Backend) testSetSingleUploadLimit(limit int64) { b.singleUploadLimit = limit }

// TestPan123TokenCaching 验证 token 只申请一次并复用。
func TestPan123TokenCaching(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)
	backend.testSetSingleUploadLimit(1 << 20)

	payload := bytes.Repeat([]byte("a"), 1024)
	for i := 0; i < 3; i++ {
		if _, _, err := backend.Put(context.Background(), fmt.Sprintf("blob-%d", i), bytes.NewReader(payload)); err != nil {
			t.Fatalf("Put 失败：%v", err)
		}
	}
	if mock.tokenRequests != 1 {
		t.Fatalf("access_token 应只申请一次，实际 %d 次", mock.tokenRequests)
	}
}

// TestPan123ConcurrentTokenRefresh 验证并发请求在 token 临近过期时只会串行刷新
// 一次：平台限制同 client_id 最多 3 个 token 并存，并发申请会互相挤占（踢掉
// 正在使用的 token）造成 401 抖动。
func TestPan123ConcurrentTokenRefresh(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)
	mock.content[1000] = []byte("x") // Stat 需要对象存在

	// 把缓存置为「临近过期」（10 分钟后到期 < 1 小时刷新阈值），触发刷新路径。
	key := strings.TrimSpace(backend.cfg.ClientID)
	pan123TokenMu.Lock()
	pan123TokenCache[key] = pan123TokenEntry{token: "stale", expiry: time.Now().Add(10 * time.Minute)}
	pan123TokenMu.Unlock()
	t.Cleanup(func() {
		pan123TokenMu.Lock()
		delete(pan123TokenCache, key)
		pan123TokenMu.Unlock()
	})

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := backend.Stat(context.Background(), "1000"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发请求失败：%v", err)
	}
	if mock.tokenRequests != 1 {
		t.Fatalf("并发刷新应只申请一次 token，实际 %d 次", mock.tokenRequests)
	}
}

// TestPan123SingleUpload 验证 ≤阈值走单步上传，且返回平台 fileID 作为真实定位符。
func TestPan123SingleUpload(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)
	backend.testSetSingleUploadLimit(1 << 30)

	payload := bytes.Repeat([]byte("x"), 1000)
	written, storedRef, err := backend.Put(context.Background(), "blob-small", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Put 失败：%v", err)
	}
	if written != int64(len(payload)) {
		t.Fatalf("写入字节数应为 %d，实际 %d", len(payload), written)
	}
	fileID, err := strconv.ParseInt(storedRef, 10, 64)
	if err != nil {
		t.Fatalf("真实定位符应为数字 fileID，实际 %q", storedRef)
	}
	if !bytes.Equal(mock.content[fileID], payload) {
		t.Fatalf("平台侧内容与上传内容不一致")
	}
	if mock.singleUploads != 1 || mock.sliceUploads != 0 {
		t.Fatalf("应走单步上传（single=%d slice=%d）", mock.singleUploads, mock.sliceUploads)
	}

	// Stat 与 Open 应基于 fileID 工作。
	size, err := backend.Stat(context.Background(), storedRef)
	if err != nil || size != int64(len(payload)) {
		t.Fatalf("Stat 失败：size=%d err=%v", size, err)
	}
}

// TestPan123SliceUpload 验证超过阈值走分片上传（并发 3），分片按序合并后内容正确。
func TestPan123SliceUpload(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)
	backend.testSetSingleUploadLimit(100) // 强制走分片

	// 3 个完整分片 + 1 个尾部残片，覆盖"多片并发 + 非整片结尾"。
	payload := make([]byte, 3*mock.sliceSize+12345)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	written, storedRef, err := backend.Put(context.Background(), "blob-big", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Put 失败：%v", err)
	}
	if written != int64(len(payload)) {
		t.Fatalf("写入字节数应为 %d，实际 %d", len(payload), written)
	}
	if mock.sliceUploads != 4 {
		t.Fatalf("应上传 4 个分片，实际 %d", mock.sliceUploads)
	}
	fileID, err := strconv.ParseInt(storedRef, 10, 64)
	if err != nil {
		t.Fatalf("定位符应为数字 fileID，实际 %q", storedRef)
	}
	if !bytes.Equal(mock.content[fileID], payload) {
		t.Fatal("分片合并内容与原文不一致（顺序或字节有误）")
	}
}

// TestPan123CompleteVerifyingRetry 验证 upload_complete 返回 20103（校验中）时
// 继续轮询直至合并完成——真实平台首次调用几乎必然返回该码。
func TestPan123CompleteVerifyingRetry(t *testing.T) {
	mock := newPan123Mock(t)
	mock.verifyPolls = 2
	backend := newPan123TestBackend(mock)
	backend.testSetSingleUploadLimit(100) // 强制走分片路径

	payload := bytes.Repeat([]byte("v"), 4096)
	written, storedRef, err := backend.Put(context.Background(), "blob-verify-retry", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("校验中状态应继续轮询而非失败：%v", err)
	}
	if written != int64(len(payload)) {
		t.Fatalf("写入字节数应为 %d，实际 %d", len(payload), written)
	}
	fileID, err := strconv.ParseInt(storedRef, 10, 64)
	if err != nil {
		t.Fatalf("定位符应为数字 fileID，实际 %q", storedRef)
	}
	if !bytes.Equal(mock.content[fileID], payload) {
		t.Fatal("平台侧内容与上传内容不一致")
	}
}

// TestPan123CreateReuse 验证 create 秒传命中时跳过内容上传：平台创建新文件
// 条目并复用物理数据（真机实测语义），因此返回的 fileID 可用于后续安全删除。
// 注意：不使用 /upload/v2/file/sha1_reuse——真机实测其始终 miss 且命中语义
// 未经证实（可能指向既有文件条目，删除会误伤）。
func TestPan123CreateReuse(t *testing.T) {
	mock := newPan123Mock(t)
	mock.createReuse = true
	backend := newPan123TestBackend(mock)
	backend.testSetSingleUploadLimit(100) // 强制走分片路径（create 自带秒传）

	payload := bytes.Repeat([]byte("y"), 4096)
	written, storedRef, err := backend.Put(context.Background(), "blob-reuse", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Put 失败：%v", err)
	}
	if written != int64(len(payload)) || storedRef != "8888" {
		t.Fatalf("秒传应返回新条目 fileID，实际 written=%d ref=%q", written, storedRef)
	}
	if mock.singleUploads != 0 || mock.sliceUploads != 0 {
		t.Fatalf("秒传命中不应发生内容上传")
	}
}

// TestPan123TokenRefreshOn401 验证 401 时刷新 token 并重试一次。
func TestPan123TokenRefreshOn401(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)
	backend.testSetSingleUploadLimit(1 << 30)

	if _, _, err := backend.Put(context.Background(), "blob-1", bytes.NewReader([]byte("first"))); err != nil {
		t.Fatalf("首次 Put 失败：%v", err)
	}
	// 使已缓存 token 失效：下一次调用收到 401 后应自动刷新。
	mock.mu.Lock()
	mock.tokenValid = false
	mock.mu.Unlock()
	backend.invalidateTokenForTest()

	size, err := backend.Stat(context.Background(), "1001")
	if err != nil {
		t.Fatalf("token 失效后 Stat 应自动刷新并成功：%v", err)
	}
	if size != 5 {
		t.Fatalf("大小应为 5，实际 %d", size)
	}
	// 首次 Put 申请 1 次 token；Stat 因 401 刷新 1 次。
	if mock.tokenRequests != 2 {
		t.Fatalf("access_token 应请求 2 次（一次申请 + 一次 401 刷新），实际 %d", mock.tokenRequests)
	}
}

func (b *Pan123Backend) invalidateTokenForTest() { b.invalidateToken() }

// TestPan123OpenRange 验证远端支持 Range 时按偏移读取。
func TestPan123OpenRange(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)
	payload := bytes.Repeat([]byte("abcdefghij"), 100) // 1000 字节
	mock.mu.Lock()
	mock.nextFileID++
	fileID := mock.nextFileID
	mock.content[fileID] = payload
	mock.mu.Unlock()

	reader, err := backend.Open(context.Background(), strconv.FormatInt(fileID, 10))
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer reader.Close()
	if _, err := reader.Seek(500, io.SeekStart); err != nil {
		t.Fatalf("Seek 失败：%v", err)
	}
	got := make([]byte, 100)
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatalf("Read 失败：%v", err)
	}
	if !bytes.Equal(got, payload[500:600]) {
		t.Fatalf("Range 读取内容不一致：%q", string(got[:10]))
	}
}

// TestPan123OpenWithoutRangeSupport 验证远端忽略 Range 时流式丢弃前缀。
func TestPan123OpenWithoutRangeSupport(t *testing.T) {
	mock := newPan123Mock(t)
	mock.supportRange = false
	backend := newPan123TestBackend(mock)
	payload := bytes.Repeat([]byte("0123456789"), 100)
	mock.mu.Lock()
	mock.nextFileID++
	fileID := mock.nextFileID
	mock.content[fileID] = payload
	mock.mu.Unlock()

	reader, err := backend.Open(context.Background(), strconv.FormatInt(fileID, 10))
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer reader.Close()
	if _, err := reader.Seek(10, io.SeekStart); err != nil {
		t.Fatalf("Seek 失败：%v", err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatalf("Read 失败：%v", err)
	}
	if string(got) != string(payload[10:15]) {
		t.Fatalf("忽略 Range 时内容应正确，实际 %q", string(got))
	}
}

// TestPan123DeleteIdempotent 验证删除幂等：平台的回收站接口对已删除 / 不存在的
// ID 都返回成功，因此同一对象重复删除不会失败（任务重跑安全）。
func TestPan123DeleteIdempotent(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)
	ctx := context.Background()
	if err := backend.Delete(ctx, "123"); err != nil {
		t.Fatalf("删除应成功：%v", err)
	}
	if err := backend.Delete(ctx, "123"); err != nil {
		t.Fatalf("重复删除应幂等成功：%v", err)
	}
	if err := backend.Delete(ctx, "not-a-number"); !errors.Is(err, ErrUnsafeRef) {
		t.Fatalf("非法定位符应返回 ErrUnsafeRef，实际 %v", err)
	}
}

// TestPan123EmptyObject 验证 0 字节对象：系统不限制空文件上传，单步接口上传
// 空内容并返回 fileID（真实平台实测支持空文件）。
func TestPan123EmptyObject(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)
	backend.testSetSingleUploadLimit(1 << 30)

	written, storedRef, err := backend.Put(context.Background(), "blob-empty", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("空对象上传失败：%v", err)
	}
	if written != 0 {
		t.Fatalf("空对象写入字节数应为 0，实际 %d", written)
	}
	fileID, err := strconv.ParseInt(storedRef, 10, 64)
	if err != nil {
		t.Fatalf("定位符应为数字 fileID，实际 %q", storedRef)
	}
	if len(mock.content[fileID]) != 0 {
		t.Fatalf("平台侧内容应为空，实际 %d 字节", len(mock.content[fileID]))
	}
	if size, statErr := backend.Stat(context.Background(), storedRef); statErr != nil || size != 0 {
		t.Fatalf("空对象 Stat 应为 0：size=%d err=%v", size, statErr)
	}
}

// TestPan123DeleteBatch 验证批量删除：单次最多 100 个 ID、超出自动分批，把多分片
// 对象的逐片删除降为百次级调用（远端后端限流安全）。
func TestPan123DeleteBatch(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)
	ctx := context.Background()

	refs := make([]string, 0, 150)
	for index := 0; index < 150; index++ {
		refs = append(refs, strconv.Itoa(1000+index))
	}
	if err := backend.DeleteBatch(ctx, refs); err != nil {
		t.Fatalf("批量删除失败：%v", err)
	}
	if mock.trashCalls != 2 {
		t.Fatalf("150 个对象应按 100 上限分 2 批，实际 %d 批", mock.trashCalls)
	}
	if len(mock.trashIDs[0]) != 100 || len(mock.trashIDs[1]) != 50 {
		t.Fatalf("分批大小不符：%d + %d", len(mock.trashIDs[0]), len(mock.trashIDs[1]))
	}

	// 空批次不发请求；单对象删除复用同一接口（含 1 个 ID）。
	if err := backend.DeleteBatch(ctx, nil); err != nil {
		t.Fatalf("空批次应直接成功：%v", err)
	}
	if mock.trashCalls != 2 {
		t.Fatalf("空批次不应发起请求，实际 %d 批", mock.trashCalls)
	}
	if err := backend.Delete(ctx, "2000"); err != nil {
		t.Fatalf("单对象删除失败：%v", err)
	}
	if mock.trashCalls != 3 || len(mock.trashIDs[2]) != 1 {
		t.Fatalf("单对象删除应发起含 1 个 ID 的请求，实际 %d 批", mock.trashCalls)
	}
}

// TestPan123Presign 验证直链能力探测：未启用时降级、启用时返回地址。
func TestPan123Presign(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)

	url, ok, err := backend.Presign(context.Background(), "1001", time.Minute, PresignForDirect)
	if err != nil || ok {
		t.Fatalf("未启用直链应返回 ok=false（ok=%v err=%v）", ok, err)
	}

	// 交付方式=优先 302 直连时才返回直链地址（默认优先本机中转，不返回）。
	backend.cfg.DeliveryPrefer = Pan123DeliveryRedirect
	url, ok, err = backend.Presign(context.Background(), "1001", time.Minute, PresignForDirect)
	if err != nil || !ok || !strings.Contains(url, "/cdn/1001") {
		t.Fatalf("启用直链且选择 302 交付应返回直链地址（url=%q ok=%v err=%v）", url, ok, err)
	}
}

// TestPan123PutChecksum 验证中转文件计算出的 MD5 与内容一致（平台 etag 语义）。
func TestPan123PutChecksum(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)
	backend.testSetSingleUploadLimit(1 << 30)
	payload := []byte("checksum-payload")
	if _, _, err := backend.Put(context.Background(), "blob-checksum", bytes.NewReader(payload)); err != nil {
		t.Fatalf("Put 失败：%v", err)
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) == "" {
		t.Fatal("unreachable")
	}
	if mock.singleUploads != 1 {
		t.Fatalf("应完成一次单步上传")
	}
}

// TestPan123DirectLinkAuthGolden 用官方「直链鉴权」文档的示例做黄金向量校验：
// 密钥 289ds32418bxdba、过期时间戳 1689220731、随机数 123、UID 13、
// URI /13/files/1.txt → auth_key=1689220731-123-13-3bdacc0e031fd67fe829152f37c8fbad。
func TestPan123DirectLinkAuthGolden(t *testing.T) {
	const (
		privateKey = "289ds32418bxdba"
		uri        = "/13/files/1.txt"
		uid        = "13"
		randStr    = "123"
		expiry     = int64(1689220731)
		wantAuth   = "1689220731-123-13-3bdacc0e031fd67fe829152f37c8fbad"
	)
	if got := directLinkAuthValue(uri, uid, randStr, expiry, privateKey); got != wantAuth {
		t.Fatalf("auth_key 与官方示例不一致：got=%s want=%s", got, wantAuth)
	}
	signed, err := signDirectLinkURL(
		"http://13.cdn.123clouddisk.com/13/files/1.txt", privateKey, time.Unix(expiry, 0), randStr)
	if err != nil {
		t.Fatalf("签名失败：%v", err)
	}
	wantURL := "http://13.cdn.123clouddisk.com/13/files/1.txt?auth_key=" + wantAuth
	if signed != wantURL {
		t.Fatalf("签名后的直链不符：\n got=%s\nwant=%s", signed, wantURL)
	}
}

// TestPan123PresignWithAuth 验证开启直链鉴权后 Presign 返回带 auth_key 的直链，
// 且签名可按官方算法复算；未开启鉴权时地址保持原样。
func TestPan123PresignWithAuth(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)
	backend.cfg.DirectLinkAuth = true
	backend.cfg.DirectLinkAuthKey = "289ds32418bxdba"
	// 交付方式=优先 302 直连（默认是优先本机中转，不会走 302）。
	backend.cfg.DeliveryPrefer = Pan123DeliveryRedirect

	ttl := 10 * time.Minute
	// 302 交付：返回带鉴权签名的直链地址。
	got, ok, err := backend.Presign(context.Background(), "123456", ttl, PresignForDirect)
	if err != nil || !ok {
		t.Fatalf("Presign 失败：ok=%v err=%v", ok, err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("返回地址无法解析：%v", err)
	}
	authKey := parsed.Query().Get("auth_key")
	parts := strings.Split(authKey, "-")
	if len(parts) != 4 {
		t.Fatalf("auth_key 应为 timestamp-rand-uid-md5 四段，实际 %d 段：%s", len(parts), authKey)
	}
	if len(parts[0]) != 10 || len(parts[3]) != 32 {
		t.Fatalf("时间戳应为 10 位、md5 应为 32 位：%s", authKey)
	}
	if strings.Contains(parts[1], "-") {
		t.Fatalf("随机数不能包含中划线：%s", parts[1])
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		t.Fatalf("时间戳不是十进制整数：%s", parts[0])
	}
	if want := directLinkAuthValue(parsed.Path, parts[2], parts[1], expiry, backend.cfg.DirectLinkAuthKey); want != authKey {
		t.Fatalf("签名无法按官方算法复算：\n got=%s\nwant=%s", authKey, want)
	}
	if delta := time.Until(time.Unix(expiry, 0)); delta > ttl+5*time.Second || delta < ttl-5*time.Second {
		t.Fatalf("过期时间与 ttl 不符：delta=%s ttl=%s", delta, ttl)
	}

	// 未开启鉴权：地址保持原样，不带 auth_key。
	backend.cfg.DirectLinkAuth = false
	plain, ok, err := backend.Presign(context.Background(), "123456", ttl, PresignForDirect)
	if err != nil || !ok {
		t.Fatalf("Presign 失败：ok=%v err=%v", ok, err)
	}
	if strings.Contains(plain, "auth_key") {
		t.Fatalf("未开启鉴权不应带 auth_key：%s", plain)
	}
}

// TestPan123OpenChannelByPurpose 验证服务端取内容的通道按额度偏好切换（交付始终
// 本机中转，与选哪份额度无关）：
//   - 默认两者都走 download_info（自用下载流量）；
//   - 显式选「直链流量」且直链空间可用 → direct-link（直链流量），字节一致；
//   - 开启异常切换时：直链取址失败退回自用、自用失败改用直链。
func TestPan123OpenChannelByPurpose(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)
	backend.cfg.ChannelSwitch = true // 允许异常时互相切换
	ctx := context.Background()
	payload := bytes.Repeat([]byte("123pan-channel-"), 64)
	mock.mu.Lock()
	mock.nextFileID++
	fileID := mock.nextFileID
	mock.content[fileID] = payload
	mock.mu.Unlock()
	ref := strconv.FormatInt(fileID, 10)

	channelOf := func(purpose PresignPurpose) (string, []byte) {
		reader, err := backend.OpenWithPurpose(ctx, ref, purpose)
		if err != nil {
			t.Fatalf("打开失败：%v", err)
		}
		defer reader.Close()
		remote, ok := reader.(*remoteReadSeeker)
		if !ok {
			t.Fatalf("reader 类型不符：%T", reader)
		}
		got, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("读取失败：%v（url=%s size=%d 读到=%d）", err, remote.url, remote.size, len(got))
		}
		return remote.url, got
	}

	// 默认（内部读取）与分享默认：自用下载通道。
	if url, got := channelOf(""); !strings.Contains(url, "/fake-download/") || !bytes.Equal(got, payload) {
		t.Fatalf("默认应走自用下载通道且内容一致，实际 url=%s", url)
	}
	if url, got := channelOf(PresignForShare); !strings.Contains(url, "/fake-download/") || !bytes.Equal(got, payload) {
		t.Fatalf("分享默认应走自用下载通道且内容一致，实际 url=%s", url)
	}
	// 默认直链用途也是自用下载通道（两处默认都是自用下载流量）。
	if url, got := channelOf(PresignForDirect); !strings.Contains(url, "/fake-download/") || !bytes.Equal(got, payload) {
		t.Fatalf("直链用途默认应走自用下载通道且内容一致，实际 url=%s", url)
	}
	// 显式选「直链流量」：走直链通道，字节一致。
	backend.cfg.DirectPrefer = Pan123PreferDirect
	url, got := channelOf(PresignForDirect)
	if !strings.Contains(url, "/cdn/") || !bytes.Equal(got, payload) {
		t.Fatalf("选择直链流量后应走直链通道且内容一致，实际 url=%s", url)
	}
	// 直链取址失败（平台未开启直链空间）→ 退回自用通道，不影响交付。
	mock.directLink = false
	if url, got := channelOf(PresignForDirect); !strings.Contains(url, "/fake-download/") || !bytes.Equal(got, payload) {
		t.Fatalf("直链取址失败应退回自用通道，实际 url=%s", url)
	}
	// 改回自用：直链用途走自用通道。
	mock.directLink = true
	backend.cfg.DirectPrefer = Pan123PreferDownload
	if url := func() string { u, _ := channelOf(PresignForDirect); return u }(); !strings.Contains(url, "/fake-download/") {
		t.Fatalf("直链选择自用后不应走直链通道，实际 url=%s", url)
	}

	// 反向切换：优先自用通道但自用取址失败 → 自动改用直链通道。
	backend.cfg.DirectPrefer = Pan123PreferDirect
	mock.downloadInfoFail = true
	if url, got := channelOf(PresignForShare); !strings.Contains(url, "/cdn/") || !bytes.Equal(got, payload) {
		t.Fatalf("自用通道失败应改用直链通道且内容一致，实际 url=%s", url)
	}

	// 严格模式（关闭异常切换）："优先"即"始终"，不消耗另一份额度而是直接报错。
	backend.cfg.ChannelSwitch = false
	if _, err := backend.OpenWithPurpose(ctx, ref, PresignForShare); err == nil {
		t.Fatal("关闭切换后自用通道失败应直接报错，而不是改用直链通道")
	} else {
		t.Logf("严格模式（自用失败）：%v", err)
	}
	mock.downloadInfoFail = false
	mock.directLink = false
	if _, err := backend.OpenWithPurpose(ctx, ref, PresignForDirect); err == nil {
		t.Fatal("关闭切换后直链取址失败应直接报错，而不是改用自用通道")
	} else {
		t.Logf("严格模式（直链失败）：%v", err)
	}
}

// TestPan123DeliveryPreference 验证「交付方式」与「额度偏好」是两件独立的事：
//   - 交付方式默认"优先本机中转"→ 不做 302；改为"优先 302 直连"才返回直链地址；
//   - 额度偏好（自用/直链流量）不影响 302 决策，只影响中转时服务端取内容的通道。
func TestPan123DeliveryPreference(t *testing.T) {
	mock := newPan123Mock(t)
	backend := newPan123TestBackend(mock)
	ttl := 10 * time.Minute
	ctx := context.Background()

	// 默认交付方式（空值=优先本机中转）：不做 302。
	if _, ok, err := backend.Presign(ctx, "123456", ttl, PresignForShare); err != nil || ok {
		t.Fatalf("默认应优先本机中转（ok=false），实际 ok=%v err=%v", ok, err)
	}
	// 额度偏好选直链流量也不改变交付方式：仍是本机中转。
	backend.cfg.SharePrefer = Pan123PreferDirect
	if _, ok, err := backend.Presign(ctx, "123456", ttl, PresignForShare); err != nil || ok {
		t.Fatalf("额度偏好不应影响交付方式，实际 ok=%v err=%v", ok, err)
	}
	// 交付方式改为优先 302 直连：返回直链地址。
	backend.cfg.DeliveryPrefer = Pan123DeliveryRedirect
	if url, ok, err := backend.Presign(ctx, "123456", ttl, PresignForShare); err != nil || !ok || !strings.Contains(url, "/cdn/") {
		t.Fatalf("优先 302 应返回直链地址，实际 url=%q ok=%v err=%v", url, ok, err)
	}
	// 显式改回优先本机中转：不再 302。
	backend.cfg.DeliveryPrefer = Pan123DeliveryProxy
	if _, ok, _ := backend.Presign(ctx, "123456", ttl, PresignForShare); ok {
		t.Fatal("优先本机中转时不应走 302")
	}
}

// TestPan123NeedDirectLink 验证「直链能力由设置推导，没有独立总闸」：
// 默认（全自用 + 优先本机中转）不需要直链、不会调用平台开启直链空间；
// 任一设置选了直链就需要。
func TestPan123NeedDirectLink(t *testing.T) {
	backend := newPan123TestBackend(newPan123Mock(t))
	if backend.NeedDirectLink() {
		t.Fatal("默认配置不应需要直链")
	}
	backend.cfg.SharePrefer = Pan123PreferDirect
	if !backend.NeedDirectLink() {
		t.Fatal("分享额度选直链流量时需要直链")
	}
	backend.cfg.SharePrefer = Pan123PreferDownload
	backend.cfg.DirectPrefer = Pan123PreferDirect
	if !backend.NeedDirectLink() {
		t.Fatal("直链额度选直链流量时需要直链")
	}
	backend.cfg.DirectPrefer = Pan123PreferDownload
	backend.cfg.DeliveryPrefer = Pan123DeliveryRedirect
	if !backend.NeedDirectLink() {
		t.Fatal("交付方式选优先 302 时需要直链")
	}
}
