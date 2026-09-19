package blobstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// S3Backend 把对象存储到 S3 兼容服务（AWS S3 / MinIO / Cloudflare R2 / 阿里云
// OSS / 腾讯云 COS 等）。
//
// 实现说明：
//   - 手写 AWS Signature V4 签名，不引入重型 SDK（与项目"最少依赖"取向一致）；
//   - 写入前先把数据落到本机中转文件，从而拿到内容 SHA-256 用于头签名与完整性
//     校验（流式签名需要分块签名，复杂度与本场景收益不成比例）；
//   - 读取使用普通 GET + Range，每次请求前重新签名（签名覆盖 Range 头）；
//   - Presign 输出带查询签名的直链，供「302 交付」使用；调用方 URL 有效期建议
//     不超过 7 天（平台上限）。
type S3Backend struct {
	cfg    S3Config
	client *http.Client
}

// S3Credentials 是对象存储的访问凭据（来自 .env，绝不入库、不写日志）。
type S3Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
}

// S3Config 是对象存储后端的配置：凭据来自 .env，其余来自
// storage_backends.settings（管理员在存储管理中填写）。
type S3Config struct {
	// Endpoint 形如 https://minio.example.com（不含 bucket，可带端口）。
	Endpoint string
	// Region 默认 us-east-1。
	Region string
	Bucket string
	// Prefix 是对象键前缀（可选，便于与其它系统共用 bucket）。
	Prefix string
	// PathStyle 为真时使用 path-style 寻址（MinIO/R2 建议开启；AWS 新桶需关闭）。
	PathStyle bool
	// SpoolDir 是大文件中转目录（默认系统临时目录）。
	SpoolDir string

	Credentials S3Credentials
}

const (
	s3UnsignedPayload = "UNSIGNED-PAYLOAD"
	s3Service         = "s3"
	// s3MaxPresignTTL 平台允许的最长预签名有效期（7 天）。
	s3MaxPresignTTL = 7 * 24 * time.Hour
)

var (
	// ErrS3Config 表示对象存储配置不完整。
	ErrS3Config = errors.New("blobstore: 对象存储配置不完整")
)

func NewS3Backend(cfg S3Config) *S3Backend {
	return &S3Backend{
		cfg: cfg,
		client: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          16,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		}},
	}
}

func (b *S3Backend) Kind() string { return "s3" }

// PresignEnabled 实现 blobstore 的直链就绪判定：配置完整即具备预签名直链能力。
func (b *S3Backend) PresignEnabled() bool { return b.Configured() == nil }

// Configured 校验配置完整性（Reload 时用于跳过未配置完善的后端）。
func (b *S3Backend) Configured() error {
	if strings.TrimSpace(b.cfg.Endpoint) == "" {
		return fmt.Errorf("%w: settings.endpoint 未配置", ErrS3Config)
	}
	parsed, err := url.Parse(strings.TrimSpace(b.cfg.Endpoint))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%w: settings.endpoint 必须是 http(s) 地址", ErrS3Config)
	}
	if strings.TrimSpace(b.cfg.Bucket) == "" {
		return fmt.Errorf("%w: settings.bucket 未配置", ErrS3Config)
	}
	if strings.TrimSpace(b.cfg.Credentials.AccessKeyID) == "" ||
		strings.TrimSpace(b.cfg.Credentials.SecretAccessKey) == "" {
		return fmt.Errorf("%w: 缺少 XPH_S3_ACCESS_KEY_ID / XPH_S3_SECRET_ACCESS_KEY", ErrS3Config)
	}
	return nil
}

// Put 写入对象：中转到本机临时文件（计算 SHA-256）后整对象 PUT。
// 真实定位符就是 ref（对象键），因此直接返回 ref。
func (b *S3Backend) Put(ctx context.Context, ref string, r io.Reader) (int64, string, error) {
	if err := b.Configured(); err != nil {
		return 0, "", err
	}
	spoolPath, size, sum, err := b.spool(r)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = os.Remove(spoolPath) }()

	file, err := os.Open(spoolPath)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	request, err := b.newRequest(ctx, http.MethodPut, b.objectURL(b.objectKey(ref)))
	if err != nil {
		return 0, "", err
	}
	request.Body = file
	request.ContentLength = size
	request.Header.Set("Content-Type", "application/octet-stream")
	b.sign(request, sum, time.Now())

	response, err := b.client.Do(request)
	if err != nil {
		return 0, "", fmt.Errorf("blobstore: 对象存储上传失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return 0, "", fmt.Errorf("blobstore: 对象存储上传失败（HTTP %d：%s）",
			response.StatusCode, bodyTail(body))
	}
	return size, ref, nil
}

// Open 返回按 Range 拉取的远端读取流（签名在每次请求前重算）。
func (b *S3Backend) Open(ctx context.Context, ref string) (io.ReadSeekCloser, error) {
	key := b.objectKey(ref)
	size, err := b.statKey(ctx, key)
	if err != nil {
		return nil, err
	}
	return &remoteReadSeeker{
		ctx: ctx, client: b.client, url: b.objectURL(key).String(), size: size,
		prepare: func(request *http.Request) error {
			b.sign(request, s3UnsignedPayload, time.Now())
			return nil
		},
	}, nil
}

func (b *S3Backend) Stat(ctx context.Context, ref string) (int64, error) {
	return b.statKey(ctx, b.objectKey(ref))
}

func (b *S3Backend) Delete(ctx context.Context, ref string) error {
	request, err := b.newRequest(ctx, http.MethodDelete, b.objectURL(b.objectKey(ref)))
	if err != nil {
		return err
	}
	b.sign(request, s3UnsignedPayload, time.Now())
	response, err := b.client.Do(request)
	if err != nil {
		return fmt.Errorf("blobstore: 对象存储删除失败：%w", err)
	}
	defer response.Body.Close()
	// S3 的 DELETE 幂等：对象不存在也返回 204。
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("blobstore: 对象存储删除失败（HTTP %d：%s）",
			response.StatusCode, bodyTail(body))
	}
	return nil
}

// Presign 生成带查询签名的 GET 直链（302 交付使用）。
// Presign 生成预签名地址。purpose 只对 123 云盘有意义（两份额度二选一），
// 对象存储没有额度区分，忽略该参数。
func (b *S3Backend) Presign(ctx context.Context, ref string, ttl time.Duration, _ PresignPurpose) (string, bool, error) {
	if err := b.Configured(); err != nil {
		return "", false, err
	}
	if ttl <= 0 || ttl > s3MaxPresignTTL {
		ttl = time.Hour
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	region := b.region()
	scope := strings.Join([]string{dateStamp, region, s3Service, "aws4_request"}, "/")

	target := b.objectURL(b.objectKey(ref))
	query := url.Values{}
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-Credential", b.cfg.Credentials.AccessKeyID+"/"+scope)
	query.Set("X-Amz-Date", amzDate)
	query.Set("X-Amz-Expires", strconv.Itoa(int(ttl.Seconds())))
	query.Set("X-Amz-SignedHeaders", "host")
	canonicalQuery := canonicalQueryString(query)

	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		canonicalURIPath(target),
		canonicalQuery,
		"host:" + target.Host + "\n",
		"host",
		s3UnsignedPayload,
	}, "\n")
	signature := b.signature(dateStamp, region, canonicalRequest, amzDate)
	query.Set("X-Amz-Signature", signature)
	target.RawQuery = canonicalQueryString(query)
	return target.String(), true, nil
}

// --- 内部实现 ---

func (b *S3Backend) region() string {
	if strings.TrimSpace(b.cfg.Region) == "" {
		return "us-east-1"
	}
	return strings.TrimSpace(b.cfg.Region)
}

// objectKey 生成对象的存储键：prefix/ref（去掉多余斜杠）。
func (b *S3Backend) objectKey(ref string) string {
	prefix := strings.Trim(strings.TrimSpace(b.cfg.Prefix), "/")
	if prefix == "" {
		return ref
	}
	return prefix + "/" + strings.TrimPrefix(ref, "/")
}

// objectURL 生成对象地址（path-style 或 virtual-hosted）。
func (b *S3Backend) objectURL(key string) *url.URL {
	endpoint, _ := url.Parse(strings.TrimSpace(b.cfg.Endpoint))
	target := *endpoint
	encodedKey := encodePathSegments(key)
	if b.cfg.PathStyle {
		target.Path = strings.TrimRight(endpoint.Path, "/") + "/" + b.cfg.Bucket + "/" + key
	} else {
		target.Host = b.cfg.Bucket + "." + endpoint.Host
		target.Path = strings.TrimRight(endpoint.Path, "/") + "/" + key
	}
	target.RawPath = strings.TrimRight(endpoint.EscapedPath(), "/") +
		func() string {
			if b.cfg.PathStyle {
				return "/" + b.cfg.Bucket
			}
			return ""
		}() + "/" + encodedKey
	target.RawQuery = ""
	return &target
}

// copySource 生成 CopyObject 的 x-amz-copy-source（/bucket/key，路径已编码）。
func (b *S3Backend) copySource(key string) string {
	return "/" + b.cfg.Bucket + "/" + encodePathSegments(key)
}

func (b *S3Backend) newRequest(ctx context.Context, method string, target *url.URL) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, method, target.String(), nil)
}

func (b *S3Backend) statKey(ctx context.Context, key string) (int64, error) {
	request, err := b.newRequest(ctx, http.MethodHead, b.objectURL(key))
	if err != nil {
		return 0, err
	}
	b.sign(request, s3UnsignedPayload, time.Now())
	response, err := b.client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("blobstore: 对象存储查询失败：%w", err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		size, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("blobstore: 对象存储返回的大小无效")
		}
		return size, nil
	case http.StatusNotFound:
		return 0, ErrBlobNotFound
	default:
		return 0, fmt.Errorf("blobstore: 对象存储查询失败（HTTP %d）", response.StatusCode)
	}
}

// spool 把输入流写到本机中转文件并计算 SHA-256。
func (b *S3Backend) spool(r io.Reader) (string, int64, string, error) {
	dir := b.cfg.SpoolDir
	if dir == "" {
		dir = os.TempDir()
	}
	file, err := os.CreateTemp(dir, "s3-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("创建上传中转文件失败：%w", err)
	}
	path := file.Name()
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", 0, "", err
	}
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hasher), r)
	if err != nil {
		return "", 0, "", err
	}
	if err := file.Sync(); err != nil {
		return "", 0, "", err
	}
	ok = true
	return path, size, hex.EncodeToString(hasher.Sum(nil)), nil
}

// sign 计算并写入 AWS Signature V4 头。payloadHash 为内容 SHA-256 或
// UNSIGNED-PAYLOAD；签名覆盖 host、range（若存在）与 x-amz-* 头。
func (b *S3Backend) sign(request *http.Request, payloadHash string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")
	region := b.region()
	request.Header.Set("x-amz-date", amzDate)
	request.Header.Set("x-amz-content-sha256", payloadHash)

	signed := map[string]string{"host": request.Host}
	if signed["host"] == "" {
		signed["host"] = request.URL.Host
	}
	if value := request.Header.Get("Range"); value != "" {
		signed["range"] = value
	}
	if value := request.Header.Get("x-amz-copy-source"); value != "" {
		signed["x-amz-copy-source"] = value
	}
	signed["x-amz-content-sha256"] = payloadHash
	signed["x-amz-date"] = amzDate

	headerNames := make([]string, 0, len(signed))
	for name := range signed {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	canonicalHeaders := strings.Builder{}
	for _, name := range headerNames {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(signed[name]))
		canonicalHeaders.WriteString("\n")
	}
	signedHeaders := strings.Join(headerNames, ";")

	canonicalRequest := strings.Join([]string{
		request.Method,
		canonicalURIPath(request.URL),
		canonicalQueryString(request.URL.Query()),
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")
	scope := strings.Join([]string{dateStamp, region, s3Service, "aws4_request"}, "/")
	signature := b.signature(dateStamp, region, canonicalRequest, amzDate)
	request.Header.Set("Authorization", strings.Join([]string{
		"AWS4-HMAC-SHA256 Credential=" + b.cfg.Credentials.AccessKeyID + "/" + scope,
		"SignedHeaders=" + signedHeaders,
		"Signature=" + signature,
	}, ", "))
}

// signature 生成 SigV4 签名（StringToSign → 派生密钥 → HMAC）。
func (b *S3Backend) signature(dateStamp, region, canonicalRequest, amzDate string) string {
	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		strings.Join([]string{dateStamp, region, s3Service, "aws4_request"}, "/"),
		hex.EncodeToString(canonicalHash[:]),
	}, "\n")
	secret := []byte(b.cfg.Credentials.SecretAccessKey)
	key := hmacSHA256([]byte("AWS4"+string(secret)), dateStamp)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, s3Service)
	key = hmacSHA256(key, "aws4_request")
	return hex.EncodeToString(hmacSHA256(key, stringToSign))
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(value))
	return mac.Sum(nil)
}

// canonicalURIPath 返回 SigV4 规范化的 URI 路径（保留 /，其余按 RFC3986 编码）。
func canonicalURIPath(target *url.URL) string {
	path := target.EscapedPath()
	if path == "" {
		return "/"
	}
	// url.EscapedPath 已做百分号编码；SigV4 要求未编码字符集为 A-Za-z0-9-_.~，与
	// Go 的编码规则一致，因此直接使用（但需要把编码后的 "%2F" 还原为 "/"）。
	return strings.ReplaceAll(path, "%2F", "/")
}

// canonicalQueryString 按 SigV4 规范构建查询串：按键排序、RFC3986 编码。
func canonicalQueryString(query url.Values) string {
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		values := append([]string(nil), query[key]...)
		sort.Strings(values)
		for _, value := range values {
			parts = append(parts, sigV4Escape(key)+"="+sigV4Escape(value))
		}
	}
	return strings.Join(parts, "&")
}

func encodePathSegments(path string) string {
	segments := strings.Split(path, "/")
	for index, segment := range segments {
		segments[index] = sigV4Escape(segment)
	}
	return strings.Join(segments, "/")
}

// sigV4Escape 实现 RFC3986 编码（空格为 %20，保留 A-Za-z0-9-_.~）。
func sigV4Escape(value string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	var builder strings.Builder
	for _, b := range []byte(value) {
		if strings.IndexByte(unreserved, b) >= 0 {
			builder.WriteByte(b)
			continue
		}
		builder.WriteString("%")
		builder.WriteString(strings.ToUpper(hex.EncodeToString([]byte{b})))
	}
	return builder.String()
}
