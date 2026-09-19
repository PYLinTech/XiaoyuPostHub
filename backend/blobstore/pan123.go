package blobstore

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Pan123Backend 把对象存储到 123 云盘（开放平台 OpenAPI）。
//
// 设计要点（依据官方 OpenAPI 文档）：
//
//   - 业务接口固定使用 https://open-api.123pan.com；上传域名动态获取
//     （单步上传取 /upload/v2/file/domain，分片上传取 /upload/v2/file/create
//     响应中的 servers）。
//   - access_token 30 天有效且同一 client_id 最多 3 个并存，因此必须缓存复用；
//     client_secret 只来自 .env，绝不写入数据库与日志。
//   - 上传统一走 V2：≤1GB 单步上传，>1GB（上限 10GB）分片上传；写入前先把
//     数据落到本机中转文件，以便计算平台要求的 MD5。
//   - 平台用数字 fileID 定位文件，因此 Put 返回的真实定位符是 fileID 字符串，
//     调用方给定 ref 仅作为上传文件名（随机 token，天然满足平台命名限制）。
//   - 302 直跳依赖「直链空间」（需对存储根目录 enable），未启用时 Presign
//     返回 ok=false，交付层自动降级为本机中转。
type Pan123Backend struct {
	cfg        Pan123Config
	baseURL    string
	httpClient *http.Client
	retryDelay time.Duration // 测试注入；0 时使用默认指数退避
	// singleUploadLimit 单步上传阈值（默认 1GB，测试可注入更小值）。
	singleUploadLimit int64
}

// pan123Tokens 是进程级 access_token 缓存：storage_backends 重新加载会重建后端
// 实例，缓存必须放在包级才能跨重建复用。平台限制同一 client_id 最多 3 个 token
// 并存（新 token 会让旧 token 失效），频繁申请会造成互相踢下线。
var (
	pan123TokenMu    sync.Mutex
	pan123TokenCache = map[string]pan123TokenEntry{}
	// pan123RefreshMu 串行化进程内的 token 申请：同一 client_id 在平台最多 3 个
	// token 并存，并发刷新会互相挤占（踢掉正在使用的 token），造成 401 抖动。
	pan123RefreshMu sync.Mutex
)

type pan123TokenEntry struct {
	token  string
	expiry time.Time
}

// Pan123Credentials 是 123 云盘开放平台应用凭据（来自 .env，绝不入库、绝不
// 写日志）。
type Pan123Credentials struct {
	ClientID     string
	ClientSecret string
}

// Pan123Config 是 123 云盘后端的非敏感配置。凭据来自 .env，其余来自
// storage_backends.settings（管理员在存储设置中填写）。
type Pan123Config struct {
	ClientID     string
	ClientSecret string
	// ParentFileID 是存储根目录在 123 云盘中的文件夹 ID（根目录 0 不支持直链，
	// 必须配置一个真实文件夹）。
	ParentFileID int64
	// 直链能力不再有独立开关：只要任一设置需要直链（交付方式=优先 302，或分享/直链
	// 的额度偏好选了直链流量），加载时就会自动为存储根目录启用平台侧直链空间。
	// DirectLinkAuth 为真时为直链追加 URL 鉴权签名（防盗链，需在 123 云盘直链管理
	// 的「鉴权管理」中开启并配置同一密钥，否则云盘会拒绝带签名的请求）。
	DirectLinkAuth bool
	// DirectLinkAuthKey 是直链鉴权密钥（私有密钥）。只保存在服务端配置中，不回传
	// 浏览器、不写日志。
	DirectLinkAuthKey string
	// SharePrefer / DirectPrefer 指定本机中转时服务端取内容优先走哪条通道：
	// Pan123PreferDownload（自用下载流量，download_info）或 Pan123PreferDirect
	// （直链流量，direct-link）。空值按默认：两者都用自用下载流量。
	//
	// 注意：它只决定"消耗哪份额度"，不决定交付方式（是否 302 见 DeliveryPrefer）。
	SharePrefer  string
	DirectPrefer string
	// DeliveryPrefer 指定交付方式：Pan123DeliveryProxy（优先本机中转，默认）或
	// Pan123DeliveryRedirect（优先 302 直连第三方）。
	DeliveryPrefer string
	// ChannelSwitch 为真时：优先通道不可用（取址失败/未启用）会自动改用另一条通道，
	// 交付不中断。为假时"优先"即"始终"——严格使用所选通道，不可用直接报错，绝不
	// 消耗另一份额度（便于管理员判断额度是否用完）。零值即默认：关闭。
	ChannelSwitch bool
	// BaseURL 仅用于测试注入（默认官方地址）。
	BaseURL string
	// SpoolDir 是大文件中转目录（默认系统临时目录）。
	SpoolDir string
}

const (
	pan123BaseURL = "https://open-api.123pan.com"
	// pan123SingleUploadLimit 单步上传上限：按文档口径的十进制 1GB（保守取值；
	// 若按 1GiB 处理，恰好配置 1GiB 存储分片的对象会以 1.07GB 走单步上传而
	// 可能被平台拒绝——走分片上传只是多两次调用，代价远小于失败）。
	pan123SingleUploadLimit = 1_000_000_000
	// pan123MaxFileSize 平台单文件上限：10GB（文档口径，此处为平台能力描述；
	// 由于系统强制分片（单物理对象 ≤1GiB），正常路径不会触达该上限）。
	pan123MaxFileSize = 10_000_000_000
	// pan123CompletePollInterval 官方要求 upload_complete 轮询间隔 1 秒。
	pan123CompletePollInterval = time.Second
	// pan123CompleteMaxPolls 上传完成轮询上限（约 2 分钟）。
	pan123CompleteMaxPolls = 120
	// pan123UploadVerifying 是 upload_complete 返回的"文件正在校验中"状态码。
	// 真实平台实测：分片合并的校验期间该码会先于 completed 出现，属于正常中间
	// 状态，必须继续轮询而不是当作失败。
	pan123UploadVerifying = 20103
	// pan123SliceConcurrency 分片上传并发度（保守值，避免触发限流）。
	pan123SliceConcurrency = 3
	// pan123MaxNameLength 文件名长度上限（两处文档口径 255/256，取保守值）。
	pan123MaxNameLength = 255
	// pan123TrashBatchSize 是回收站删除接口单次可传的最大文件数（平台上限 100）。
	pan123TrashBatchSize = 100
)

var (
	// ErrPan123Config 表示后端配置不完整（缺少凭据或根目录）。
	ErrPan123Config = errors.New("blobstore: 123 云盘配置不完整")
	// ErrPan123TooLarge 表示文件超过平台单文件上限（10GB）。
	ErrPan123TooLarge = errors.New("blobstore: 文件超过 123 云盘单文件上限（10GB）")
)

// pan123Error 是平台返回的业务错误（code != 0），保留 x-traceID 便于上报。
type pan123Error struct {
	Op      string
	Code    int
	Message string
	TraceID string
}

func (e *pan123Error) Error() string {
	return fmt.Sprintf("blobstore: 123 云盘 %s 失败（code=%d, message=%s, traceID=%s）",
		e.Op, e.Code, e.Message, e.TraceID)
}

func NewPan123Backend(cfg Pan123Config) *Pan123Backend {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = pan123BaseURL
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
	}
	return &Pan123Backend{
		cfg:        cfg,
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Transport: transport},
	}
}

func (b *Pan123Backend) Kind() string { return "pan123" }

// Configured 校验凭据与根目录是否可用（Reload 时用于跳过未配置完善的后端）。
func (b *Pan123Backend) Configured() error {
	if strings.TrimSpace(b.cfg.ClientID) == "" || strings.TrimSpace(b.cfg.ClientSecret) == "" {
		return fmt.Errorf("%w: 缺少 XPH_PAN123_CLIENT_ID / XPH_PAN123_CLIENT_SECRET", ErrPan123Config)
	}
	if b.cfg.ParentFileID == 0 {
		return fmt.Errorf("%w: settings.parent_file_id 未配置", ErrPan123Config)
	}
	return nil
}

// EnableDirectLink 为存储根目录启用直链空间（幂等，配置开启直链时调用）。
func (b *Pan123Backend) EnableDirectLink(ctx context.Context) error {
	var data struct {
		Enable bool `json:"enable"`
	}
	if err := b.call(ctx, http.MethodPost, "/api/v1/direct-link/enable", nil,
		map[string]any{"fileID": b.cfg.ParentFileID}, &data); err != nil {
		return err
	}
	return nil
}

// Put 写入一个对象：先把数据落到本机中转文件（计算平台要求的 MD5），再按大小
// 选择单步上传或分片上传；返回 (明文字节数, 平台 fileID 字符串)。
//
// 关于「秒传」：系统的全平台秒传（跨用户内容判重 + 引用计数复用）由数据库层
// （FindReusable）完成，命中时完全不经过本后端。这里只负责如实处理平台自身
// 的响应：/upload/v2/file/create 对已存在的相同内容可能返回 reuse=true 且不再
// 返回 preuploadID（平台内部复用物理数据，fileID 为新建条目——真机实测），
// 必须采用其结果，否则会被误判为"响应不完整"。不主动调用平台判重接口
// （/upload/v2/file/sha1_reuse 真机实测对已存在内容始终返回 reuse=false，且其
// 命中时返回的 fileID 未经证实，存在误删风险）。
func (b *Pan123Backend) Put(ctx context.Context, ref string, r io.Reader) (int64, string, error) {
	if err := b.Configured(); err != nil {
		return 0, "", err
	}
	if err := validatePan123Name(ref); err != nil {
		return 0, "", err
	}
	spoolPath, size, md5Hex, err := b.spool(r)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = os.Remove(spoolPath) }()
	if size > pan123MaxFileSize {
		return 0, "", ErrPan123TooLarge
	}

	limit := b.singleUploadLimit
	if limit <= 0 {
		limit = pan123SingleUploadLimit
	}
	var fileID int64
	if size <= limit {
		fileID, err = b.singleUpload(ctx, ref, spoolPath, size, md5Hex)
	} else {
		fileID, err = b.sliceUpload(ctx, ref, spoolPath, size, md5Hex)
	}
	if err != nil {
		return 0, "", err
	}
	if b.NeedDirectLink() {
		// 覆盖写入后直链 CDN 缓存需要失效，失败不影响上传结果。
		_ = b.refreshDirectLink(ctx)
	}
	return size, strconv.FormatInt(fileID, 10), nil
}

// Open 返回按需拉取的远端读取流（支持 Seek：Range 交付/预览/断点续传）。
// Open 打开只读流：默认走自用下载流量（download_info），内部读取（迁移、校验等）
// 不额外消耗直链额度。
func (b *Pan123Backend) Open(ctx context.Context, ref string) (io.ReadSeekCloser, error) {
	return b.OpenWithPurpose(ctx, ref, "")
}

// OpenWithPurpose 按用途选择读取通道：选「直链流量」且直链空间可用时走
// direct-link/url（消耗直链额度），否则走 download_info（自用下载流量）。
//
// 两条通道取到的是同一份字节、都支持 Range，服务端是否中转与选择哪份额度无关
// （对外始终只有一个地址）。直链取址失败时退回自用通道，不阻断交付。
func (b *Pan123Backend) OpenWithPurpose(ctx context.Context, ref string, purpose PresignPurpose) (io.ReadSeekCloser, error) {
	fileID, err := parsePan123FileID(ref)
	if err != nil {
		return nil, err
	}
	size, err := b.fileSize(ctx, fileID)
	if err != nil {
		return nil, err
	}
	preferDirect := b.preferDirectLink(purpose)
	fetchURL, preferErr := b.fetchURLForChannel(ctx, fileID, preferDirect)
	if preferErr != nil || fetchURL == "" {
		// 优先通道不可用：按开关决定是否改用另一条通道。关闭开关时"优先"即"始终"，
		// 宁可失败也不消耗另一份额度（管理员据此判断额度是否用完）。
		if !b.cfg.ChannelSwitch {
			if preferErr != nil {
				return nil, fmt.Errorf("blobstore: 123 云盘%s不可用（已关闭异常时切换）：%w",
					pan123ChannelName(preferDirect), preferErr)
			}
			return nil, fmt.Errorf("blobstore: 123 云盘%s不可用（已关闭异常时切换）", pan123ChannelName(preferDirect))
		}
		other, otherErr := b.fetchURLForChannel(ctx, fileID, !preferDirect)
		if otherErr != nil || other == "" {
			if preferErr != nil {
				return nil, preferErr
			}
			if otherErr != nil {
				return nil, otherErr
			}
			return nil, errors.New("blobstore: 123 云盘两条读取通道都不可用")
		}
		log.Printf("blobstore: 123 云盘%s不可用，本次改用%s",
			pan123ChannelName(preferDirect), pan123ChannelName(!preferDirect))
		fetchURL = other
	}
	return &remoteReadSeeker{
		ctx: ctx, client: b.httpClient, url: fetchURL, size: size,
	}, nil
}

// pan123ChannelName 返回读取通道的中文名（日志与错误文案共用）。
func pan123ChannelName(direct bool) string {
	if direct {
		return "直链通道"
	}
	return "自用下载通道"
}

// fetchURLForChannel 按通道取内容地址：direct=true 走直链（启用鉴权时自动签名），
// false 走自用下载（download_info）。直链是否可用由配置推导（NeedDirectLink）。
func (b *Pan123Backend) fetchURLForChannel(ctx context.Context, fileID int64, direct bool) (string, error) {
	if direct {
		return b.directLinkFetchURL(ctx, fileID)
	}
	return b.downloadURL(ctx, fileID)
}

// directLinkURL 获取直链地址（GET /api/v1/direct-link/url）。
func (b *Pan123Backend) directLinkURL(ctx context.Context, fileID int64) (string, error) {
	var data struct {
		URL string `json:"url"`
	}
	if err := b.call(ctx, http.MethodGet, "/api/v1/direct-link/url",
		url.Values{"fileID": []string{strconv.FormatInt(fileID, 10)}}, nil, &data); err != nil {
		return "", err
	}
	return strings.TrimSpace(data.URL), nil
}

// directLinkFetchURL 返回可直接取内容的直链地址（启用直链鉴权时自动签名）。
// 已开启鉴权的账号下未签名地址会被平台拒绝，因此服务端侧读取也必须用签名地址；
// 服务端读取是即时请求，签名有效期给足 1 小时，避免长下载中途过期。
func (b *Pan123Backend) directLinkFetchURL(ctx context.Context, fileID int64) (string, error) {
	raw, err := b.directLinkURL(ctx, fileID)
	if err != nil || raw == "" {
		return raw, err
	}
	if !b.cfg.DirectLinkAuth || strings.TrimSpace(b.cfg.DirectLinkAuthKey) == "" {
		return raw, nil
	}
	randStr, err := pan123AuthRand()
	if err != nil {
		return "", err
	}
	return signDirectLinkURL(raw, b.cfg.DirectLinkAuthKey, time.Now().Add(time.Hour), randStr)
}

func (b *Pan123Backend) Stat(ctx context.Context, ref string) (int64, error) {
	fileID, err := parsePan123FileID(ref)
	if err != nil {
		return 0, err
	}
	return b.fileSize(ctx, fileID)
}

// Delete 把对象移入云盘回收站。平台实测对「已删除 / 不存在」的 ID 也返回成功
// （code=0），删除天然幂等，任务重跑安全。
func (b *Pan123Backend) Delete(ctx context.Context, ref string) error {
	fileID, err := parsePan123FileID(ref)
	if err != nil {
		return err
	}
	return b.trash(ctx, []int64{fileID})
}

// DeleteBatch 批量移入回收站（每批 ≤ pan123TrashBatchSize，平台上限 100）：
// 用于多分片对象清理，把逐片调用降为百次级调用（大对象的逐片删除会显著放大
// API 次数并可能触发平台限流）。平台对不存在的 ID 恒返回成功，故整批调用对
// 重跑幂等。
func (b *Pan123Backend) DeleteBatch(ctx context.Context, refs []string) error {
	if len(refs) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(refs))
	for _, ref := range refs {
		fileID, err := parsePan123FileID(ref)
		if err != nil {
			return err
		}
		ids = append(ids, fileID)
	}
	for start := 0; start < len(ids); start += pan123TrashBatchSize {
		end := min(start+pan123TrashBatchSize, len(ids))
		if err := b.trash(ctx, ids[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// trash 调用删除接口（Body 为 fileIDs 数组，平台限制单次 ≤100 个）。平台实测
// 对「已删除 / 不存在」的 ID 均返回成功（code=0），无需特判错误码。
func (b *Pan123Backend) trash(ctx context.Context, fileIDs []int64) error {
	if len(fileIDs) == 0 {
		return nil
	}
	return b.call(ctx, http.MethodPost, "/api/v1/file/trash", nil,
		map[string]any{"fileIDs": fileIDs}, nil)
}

// Copy 暂不支持：物理复制由平台异步任务完成，当前系统的秒传复用走数据库
// 引用计数，无需物理复制；管理员迁移工具使用「下载 + 上传」实现。
// 123 云盘的两份额度：自用下载流量（download_info）与直链流量（direct-link）。
const (
	Pan123PreferDownload = "download"
	Pan123PreferDirect   = "direct"
)

// 交付方式：本机中转（默认）或 302 直连第三方。
const (
	Pan123DeliveryProxy    = "proxy"
	Pan123DeliveryRedirect = "redirect"
)

// preferDirectLink 判断本机中转时该用途优先走哪条读取通道（false = 自用下载流量）。
// 空值按默认：分享（含文件页下载）与站内直链都用自用下载流量。
//
// 它只影响"消耗哪份额度"，不影响交付方式：是否 302 由 DeliveryPrefer 单独决定。
func (b *Pan123Backend) preferDirectLink(purpose PresignPurpose) bool {
	prefer := b.cfg.SharePrefer
	if purpose == PresignForDirect {
		prefer = b.cfg.DirectPrefer
	}
	return prefer == Pan123PreferDirect
}

// preferRedirect 判断该后端的交付方式是否为「优先 302 直连第三方」。
// 空值按默认：优先本机中转（不消耗直链额度、不把用户跳到第三方）。
func (b *Pan123Backend) preferRedirect() bool {
	return b.cfg.DeliveryPrefer == Pan123DeliveryRedirect
}

// NeedDirectLink 表示当前配置是否要用到 123 直链——交付方式选 302，或任一用途的
// 额度偏好选了直链流量。加载后端时据此决定要不要为存储根目录启用平台侧直链空间
// （幂等，只开不关）；不需要直链时不做任何平台调用。
//
// 这里刻意不设"启用直链"总闸：需要什么能力由各设置直接推导，避免出现
// "开关没开导致其它设置静默失效"的组合状态。
func (b *Pan123Backend) NeedDirectLink() bool {
	return b.preferRedirect() ||
		b.cfg.SharePrefer == Pan123PreferDirect ||
		b.cfg.DirectPrefer == Pan123PreferDirect
}

// Presign 返回直链地址；未启用直链空间、或管理员为该用途选择了自用下载流量时
// ok=false（交付层降级本机中转）。
func (b *Pan123Backend) Presign(ctx context.Context, ref string, ttl time.Duration, purpose PresignPurpose) (string, bool, error) {
	// 交付方式独立判定：优先本机中转（默认）时不做 302，交付层会走中转；额度偏好
	// 只决定中转时服务端取内容走哪条通道（自用下载 / 直链）。
	if !b.preferRedirect() {
		return "", false, nil
	}
	_ = purpose // 交付方式与用途无关：站内直链始终中转，因此这里只会被分享/文件页调用
	fileID, err := parsePan123FileID(ref)
	if err != nil {
		return "", false, err
	}
	raw, err := b.directLinkURL(ctx, fileID)
	if err != nil {
		return "", false, err
	}
	if raw == "" {
		return "", false, nil
	}
	if !b.cfg.DirectLinkAuth || strings.TrimSpace(b.cfg.DirectLinkAuthKey) == "" {
		return raw, true, nil
	}
	// 直链 URL 鉴权：签名有效期与直链有效期一致（交付层传 10 分钟，与云盘临时
	// 地址同口径）。签名失败不能退回未签名地址——鉴权开启后未签名请求会被云盘
	// 直接拒绝，静默降级只会掩盖配置错误。
	randStr, err := pan123AuthRand()
	if err != nil {
		return "", false, err
	}
	signed, err := signDirectLinkURL(raw, b.cfg.DirectLinkAuthKey, time.Now().Add(ttl), randStr)
	if err != nil {
		return "", false, err
	}
	return signed, true, nil
}

// signDirectLinkURL 按 123 云盘 URL 鉴权规则为直链追加 auth_key 参数。
//
// 规则（官方直链鉴权文档）：
//
//		$uid.cdn.123clouddisk.com/$path?auth_key=$timestamp-$rand-$uid-$md5hash
//		$md5hash = md5(URI-timestamp-rand-uid-PrivateKey)
//
//	  - URI 是请求对象的相对地址：只含路径、不含参数，取直链自身的路径（含 $uid
//	    前缀，与原样请求的地址一致）；
//	  - uid 是云盘用户 UID，直链域名首段即 UID（如 13.cdn.123clouddisk.com）；
//	  - rand 为随机串，规则要求不能包含中划线，这里用无符号十六进制随机串；
//	  - timestamp 为过期时刻的 Unix 秒（10 位整型）。
func signDirectLinkURL(rawURL, privateKey string, expiry time.Time, randStr string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("blobstore: 直链地址无法解析：%w", err)
	}
	uid, _, _ := strings.Cut(parsed.Hostname(), ".")
	if uid == "" || parsed.Path == "" {
		return "", fmt.Errorf("blobstore: 直链地址缺少 UID 或路径，无法签名：%s", rawURL)
	}
	query := parsed.Query()
	query.Set("auth_key", directLinkAuthValue(parsed.EscapedPath(), uid, randStr, expiry.Unix(), privateKey))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// directLinkAuthValue 计算 auth_key 的值：timestamp-rand-uid-md5(URI-timestamp-rand-uid-PrivateKey)。
func directLinkAuthValue(uri, uid, randStr string, expiryUnix int64, privateKey string) string {
	payload := fmt.Sprintf("%s-%d-%s-%s-%s", uri, expiryUnix, randStr, uid, privateKey)
	sum := md5.Sum([]byte(payload))
	return fmt.Sprintf("%d-%s-%s-%x", expiryUnix, randStr, uid, sum)
}

// pan123AuthRand 生成不含中划线的随机串（官方要求 rand 不能包含中划线）。
func pan123AuthRand() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("blobstore: 生成直链鉴权随机数失败：%w", err)
	}
	return hex.EncodeToString(buf), nil
}

// spool 把输入流写到本机中转文件，同时计算平台上传所需的 MD5。
func (b *Pan123Backend) spool(r io.Reader) (string, int64, string, error) {
	dir := b.cfg.SpoolDir
	if dir == "" {
		dir = os.TempDir()
	}
	file, err := os.CreateTemp(dir, "pan123-*")
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
	md5Hash := md5.New()
	size, err := io.Copy(io.MultiWriter(file, md5Hash), r)
	if err != nil {
		return "", 0, "", err
	}
	if err := file.Sync(); err != nil {
		return "", 0, "", err
	}
	ok = true
	return path, size, hex.EncodeToString(md5Hash.Sum(nil)), nil
}

// pan123DuplicateOverwrite 重名策略：2=覆盖原文件。使用覆盖语义保证重试幂等
// （同名半成品会被替换，不会累积孤儿文件）。
const pan123DuplicateOverwrite = 2

// singleUpload 单步上传（≤1GB）：GET /upload/v2/file/domain → single/create。
func (b *Pan123Backend) singleUpload(ctx context.Context, name, spoolPath string, size int64, md5Hex string) (int64, error) {
	var domains []string
	if err := b.call(ctx, http.MethodGet, "/upload/v2/file/domain", nil, nil, &domains); err != nil {
		return 0, err
	}
	if len(domains) == 0 || strings.TrimSpace(domains[0]) == "" {
		return 0, fmt.Errorf("blobstore: 123 云盘未返回上传域名")
	}
	uploadURL := strings.TrimRight(domains[0], "/") + "/upload/v2/file/single/create"

	file, err := os.Open(spoolPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	fields := [][2]string{
		{"parentFileID", strconv.FormatInt(b.cfg.ParentFileID, 10)},
		{"filename", name},
		{"etag", md5Hex},
		{"size", strconv.FormatInt(size, 10)},
		{"duplicate", strconv.Itoa(pan123DuplicateOverwrite)},
	}
	var data struct {
		FileID    int64 `json:"fileID"`
		Completed bool  `json:"completed"`
	}
	if err := b.multipartCall(ctx, uploadURL, fields, "file", name, file, size, &data); err != nil {
		return 0, err
	}
	if !data.Completed || data.FileID == 0 {
		return 0, fmt.Errorf("blobstore: 123 云盘单步上传未完成")
	}
	return data.FileID, nil
}

// sliceUpload 分片上传（1GB ~ 10GB）：create → slice（并发 3）→ upload_complete 轮询。
func (b *Pan123Backend) sliceUpload(ctx context.Context, name, spoolPath string, size int64, md5Hex string) (int64, error) {
	var created struct {
		FileID      int64    `json:"fileID"`
		PreuploadID string   `json:"preuploadID"`
		Reuse       bool     `json:"reuse"`
		SliceSize   int64    `json:"sliceSize"`
		Servers     []string `json:"servers"`
	}
	if err := b.call(ctx, http.MethodPost, "/upload/v2/file/create", nil, map[string]any{
		"parentFileID": b.cfg.ParentFileID,
		"filename":     name,
		"etag":         md5Hex,
		"size":         size,
		"duplicate":    pan123DuplicateOverwrite,
	}, &created); err != nil {
		return 0, err
	}
	if created.Reuse && created.FileID != 0 {
		// 平台对重复内容的"已完成"响应（无 preuploadID）：如实采用其 fileID。
		// 这与系统秒传无关（系统秒传在数据库层、不经过后端）。
		return created.FileID, nil
	}
	if created.PreuploadID == "" || created.SliceSize <= 0 || len(created.Servers) == 0 {
		return 0, fmt.Errorf("blobstore: 123 云盘创建分片上传失败（响应不完整）")
	}
	server := strings.TrimRight(created.Servers[0], "/")

	file, err := os.Open(spoolPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	sliceTotal := (size + created.SliceSize - 1) / created.SliceSize
	// 有缓冲的任务通道 + ctx 退出：避免"所有工作协程失败退出后生产者永久阻塞"
	// 的死锁（无缓冲 channel 在无接收者时会挂住整个请求与文件句柄）。
	sliceNo := make(chan int64, sliceTotal)
	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	workers := pan123SliceConcurrency
	if int64(workers) > sliceTotal {
		workers = int(sliceTotal)
	}
	recordErr := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range sliceNo {
				if err := b.uploadSlice(ctx, server, file, created.PreuploadID,
					index, created.SliceSize, size); err != nil {
					recordErr(err)
				}
			}
		}()
	}
	for index := int64(0); index < sliceTotal; index++ {
		select {
		case sliceNo <- index:
		case <-ctx.Done():
			close(sliceNo)
			wg.Wait()
			return 0, ctx.Err()
		}
	}
	close(sliceNo)
	wg.Wait()
	if firstErr != nil {
		return 0, firstErr
	}

	// 上传完毕后轮询合并结果（官方要求间隔 1 秒）。平台在合并校验期间返回
	// code=20103（"文件正在校验中"）：这是正常的中间状态，必须继续轮询。
	for poll := 0; poll < pan123CompleteMaxPolls; poll++ {
		var completed struct {
			Completed bool  `json:"completed"`
			FileID    int64 `json:"fileID"`
		}
		err := b.call(ctx, http.MethodPost, "/upload/v2/file/upload_complete", nil,
			map[string]any{"preuploadID": created.PreuploadID}, &completed)
		if err == nil {
			if completed.Completed && completed.FileID != 0 {
				return completed.FileID, nil
			}
		} else {
			var apiErr *pan123Error
			if !errors.As(err, &apiErr) || apiErr.Code != pan123UploadVerifying {
				return 0, err
			}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(pan123CompletePollInterval):
		}
	}
	return 0, fmt.Errorf("blobstore: 123 云盘分片合并超时")
}

// uploadSlice 上传单片（multipart/form-data，sliceNo 从 1 开始）。
// 先流式计算分片 MD5，再流式上传，单个分片不驻留内存。
func (b *Pan123Backend) uploadSlice(ctx context.Context, server string, file *os.File,
	preuploadID string, index, sliceSize, totalSize int64) error {
	offset := index * sliceSize
	length := sliceSize
	if remaining := totalSize - offset; remaining < length {
		length = remaining
	}
	hasher := md5.New()
	if _, err := io.Copy(hasher, io.NewSectionReader(file, offset, length)); err != nil {
		return err
	}
	fields := [][2]string{
		{"preuploadID", preuploadID},
		{"sliceNo", strconv.FormatInt(index+1, 10)},
		{"sliceMD5", hex.EncodeToString(hasher.Sum(nil))},
	}
	// 平台文档规定：分片上传走 multipart/form-data，需携带鉴权头。
	return b.multipartCall(ctx, server+"/upload/v2/file/slice", fields, "slice", "blob",
		io.NewSectionReader(file, offset, length), length, nil)
}

// fileSize 查询文件大小（GET /api/v1/file/detail）。
// 注意：该接口的查询参数与响应字段都是 fileID（大写 D），而 download_info 用
// fileId（小写 d）——真实平台实测大小写敏感，两者不能混用。
func (b *Pan123Backend) fileSize(ctx context.Context, fileID int64) (int64, error) {
	var detail struct {
		Size   int64  `json:"size"`
		FileID int64  `json:"fileID"`
		Name   string `json:"filename"`
		Type   int    `json:"type"`
	}
	if err := b.call(ctx, http.MethodGet, "/api/v1/file/detail",
		url.Values{"fileID": []string{strconv.FormatInt(fileID, 10)}}, nil, &detail); err != nil {
		return 0, err
	}
	return detail.Size, nil
}

// downloadURL 获取临时下载地址（GET /api/v1/file/download_info）。
func (b *Pan123Backend) downloadURL(ctx context.Context, fileID int64) (string, error) {
	var info struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := b.call(ctx, http.MethodGet, "/api/v1/file/download_info",
		url.Values{"fileId": []string{strconv.FormatInt(fileID, 10)}}, nil, &info); err != nil {
		return "", err
	}
	if strings.TrimSpace(info.DownloadURL) == "" {
		return "", fmt.Errorf("blobstore: 123 云盘未返回下载地址（可能自用下载流量不足）")
	}
	return info.DownloadURL, nil
}

// refreshDirectLink 直链缓存刷新（覆盖写入后调用，无参数）。
func (b *Pan123Backend) refreshDirectLink(ctx context.Context) error {
	return b.call(ctx, http.MethodPost, "/api/v1/direct-link/cache/refresh", nil, nil, nil)
}

// --- 鉴权 ---

// token 返回缓存的 access_token；临近过期（1 小时）时重新申请。慢路径串行化
// 并二次检查：并发请求（如分片并发上传）不会同时申请多个 token。
func (b *Pan123Backend) token(ctx context.Context) (string, error) {
	key := strings.TrimSpace(b.cfg.ClientID)
	if token, ok := pan123CachedToken(key); ok {
		return token, nil
	}
	pan123RefreshMu.Lock()
	defer pan123RefreshMu.Unlock()
	if token, ok := pan123CachedToken(key); ok {
		return token, nil
	}
	return b.refreshAccessToken(ctx)
}

// pan123CachedToken 读取尚未临近过期的缓存 token。
func pan123CachedToken(key string) (string, bool) {
	pan123TokenMu.Lock()
	defer pan123TokenMu.Unlock()
	entry, ok := pan123TokenCache[key]
	if ok && entry.token != "" && time.Now().Before(entry.expiry.Add(-time.Hour)) {
		return entry.token, true
	}
	return "", false
}

func (b *Pan123Backend) refreshAccessToken(ctx context.Context) (string, error) {
	if strings.TrimSpace(b.cfg.ClientID) == "" || strings.TrimSpace(b.cfg.ClientSecret) == "" {
		return "", fmt.Errorf("%w: 缺少 client_id/client_secret", ErrPan123Config)
	}
	payload, err := json.Marshal(map[string]string{
		"clientID":     b.cfg.ClientID,
		"clientSecret": b.cfg.ClientSecret,
	})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.baseURL+"/api/v1/access_token", strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	request.Header.Set("Platform", "open_platform")
	request.Header.Set("Content-Type", "application/json")
	response, err := b.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("blobstore: 123 云盘获取 access_token 失败：%w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var envelope struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		TraceID string `json:"x-traceID"`
		Data    struct {
			AccessToken string `json:"accessToken"`
			ExpiredAt   string `json:"expiredAt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("blobstore: 123 云盘 access_token 响应解析失败：%w", err)
	}
	if envelope.Code != 0 || envelope.Data.AccessToken == "" {
		return "", &pan123Error{Op: "access_token", Code: envelope.Code,
			Message: envelope.Message, TraceID: envelope.TraceID}
	}
	expiry := time.Now().Add(24 * time.Hour)
	if parsed, parseErr := time.Parse(time.RFC3339, envelope.Data.ExpiredAt); parseErr == nil {
		expiry = parsed
	}
	pan123TokenMu.Lock()
	pan123TokenCache[strings.TrimSpace(b.cfg.ClientID)] = pan123TokenEntry{
		token: envelope.Data.AccessToken, expiry: expiry,
	}
	pan123TokenMu.Unlock()
	return envelope.Data.AccessToken, nil
}

// invalidateToken 丢弃缓存（401 时强制刷新）。
func (b *Pan123Backend) invalidateToken() {
	pan123TokenMu.Lock()
	delete(pan123TokenCache, strings.TrimSpace(b.cfg.ClientID))
	pan123TokenMu.Unlock()
}

// --- 统一请求层 ---

// call 发起一次 OpenAPI 调用并解析 data。规则（依据官方文档）：
//   - 携带 Authorization / Platform；
//   - code=401 时刷新 token 重试一次；
//   - code=429 时指数退避重试（最多 3 次）；
//   - 其它非 0 code 包装为 *pan123Error（保留 traceID）。
func (b *Pan123Backend) call(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	endpoint := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		endpoint = b.baseURL + path
	}
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = encoded
	}
	attempt := 0
	for {
		attempt++
		token, err := b.token(ctx)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, method, endpoint, bytesReader(payload))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Platform", "open_platform")
		request.Header.Set("Content-Type", "application/json")
		response, err := b.httpClient.Do(request)
		if err != nil {
			return fmt.Errorf("blobstore: 123 云盘请求失败（%s）：%w", path, err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		response.Body.Close()
		if readErr != nil {
			return readErr
		}
		var envelope struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
			TraceID string          `json:"x-traceID"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return fmt.Errorf("blobstore: 123 云盘响应解析失败（%s，HTTP %d）：%w", path, response.StatusCode, err)
		}
		switch envelope.Code {
		case 0:
			if out != nil && len(envelope.Data) > 0 {
				if err := json.Unmarshal(envelope.Data, out); err != nil {
					return fmt.Errorf("blobstore: 123 云盘响应解析失败（%s）：%w", path, err)
				}
			}
			return nil
		case 401:
			b.invalidateToken()
			if attempt <= 1 {
				continue // 刷新 token 后重试一次
			}
		case 429:
			if attempt < 3 {
				if err := sleepCtx(ctx, b.retryDelayFor(attempt)); err != nil {
					return err
				}
				continue
			}
		}
		return &pan123Error{Op: path, Code: envelope.Code,
			Message: envelope.Message, TraceID: envelope.TraceID}
	}
}

// multipartCall 以流式 multipart/form-data 上传（不缓存整文件到内存）。
func (b *Pan123Backend) multipartCall(ctx context.Context, endpoint string,
	fields [][2]string, fileField, fileName string, content io.Reader, contentLen int64, out any) error {
	token, err := b.token(ctx)
	if err != nil {
		return err
	}
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	writeErr := make(chan error, 1)
	go func() {
		err := func() error {
			for _, field := range fields {
				if err := multipartWriter.WriteField(field[0], field[1]); err != nil {
					return err
				}
			}
			part, err := multipartWriter.CreateFormFile(fileField, fileName)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, content); err != nil {
				return err
			}
			return multipartWriter.Close()
		}()
		// 关闭管道：请求侧据此感知结束或错误；无论哪条路径都关闭，避免写协程
		// 永久阻塞在管道上（否则每次失败都会泄漏 goroutine 与文件句柄）。
		if err != nil {
			_ = writer.CloseWithError(err)
		} else {
			_ = writer.Close()
		}
		writeErr <- err
	}()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		_ = writer.CloseWithError(err)
		<-writeErr
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Platform", "open_platform")
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	request.ContentLength = -1
	response, err := b.httpClient.Do(request)
	if err != nil {
		_ = writer.CloseWithError(err)
		<-writeErr
		return fmt.Errorf("blobstore: 123 云盘上传请求失败：%w", err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if readErr != nil {
		<-writeErr
		return readErr
	}
	// 平台在读完请求体前返回错误（如 401）时，传输层会关闭我们的管道，写协程
	// 收到 io.ErrClosedPipe——该错误不能覆盖真实的业务错误，因此只在"无业务
	// 响应体且 HTTP 非 200"时作为兜底，其余路径仅用于收敛协程。
	pipeErr := <-writeErr

	// 分片上传成功时平台返回空响应体（HTTP 200 即成功）。
	if len(strings.TrimSpace(string(raw))) == 0 {
		if response.StatusCode != http.StatusOK {
			if pipeErr != nil && !errors.Is(pipeErr, io.ErrClosedPipe) {
				return pipeErr
			}
			return fmt.Errorf("blobstore: 123 云盘上传失败（HTTP %d）", response.StatusCode)
		}
		if pipeErr != nil {
			return pipeErr
		}
		return nil
	}
	var envelope struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
		TraceID string          `json:"x-traceID"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("blobstore: 123 云盘上传响应解析失败（HTTP %d）：%w", response.StatusCode, err)
	}
	if envelope.Code != 0 {
		if envelope.Code == 401 {
			b.invalidateToken() // 下次调用重新申请 token
		}
		return &pan123Error{Op: "upload", Code: envelope.Code,
			Message: envelope.Message, TraceID: envelope.TraceID}
	}
	if out != nil && len(envelope.Data) > 0 {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}

func (b *Pan123Backend) retryDelayFor(attempt int) time.Duration {
	if b.retryDelay > 0 {
		return b.retryDelay
	}
	delay := time.Duration(1<<uint(attempt-1)) * time.Second
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	return delay
}

func sleepCtx(ctx context.Context, delay time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func bytesReader(payload []byte) io.Reader {
	if payload == nil {
		return nil
	}
	return strings.NewReader(string(payload))
}

// --- 工具 ---

func parsePan123FileID(ref string) (int64, error) {
	fileID, err := strconv.ParseInt(strings.TrimSpace(ref), 10, 64)
	if err != nil || fileID <= 0 {
		return 0, fmt.Errorf("%w: 123 云盘定位符必须是数字 fileID（收到 %q）", ErrUnsafeRef, ref)
	}
	return fileID, nil
}

// validatePan123Name 校验上传文件名符合平台限制（我们的 ref 是随机 token，
// 校验为防御性检查）。
func validatePan123Name(name string) error {
	if name == "" || len(name) > pan123MaxNameLength {
		return fmt.Errorf("%w: 文件名长度非法", ErrUnsafeRef)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: 文件名不能全为空格", ErrUnsafeRef)
	}
	const forbidden = "\"\\/:*?|><"
	if strings.ContainsAny(name, forbidden) {
		return fmt.Errorf("%w: 文件名包含平台禁用字符", ErrUnsafeRef)
	}
	return nil
}
