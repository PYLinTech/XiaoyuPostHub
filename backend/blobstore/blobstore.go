// Package blobstore 提供物理对象（blob）的统一读写、分片与生命周期管理。
//
// 设计要点：
//   - 物理对象与逻辑资源分离：resources.blob_id 指向 storage_blobs，多个资源可以
//     引用同一对象（秒传复用），引用计数归零的对象标记为 orphaned，物理清理只由
//     管理员执行。
//   - 后端抽象（本机 / 对象存储 / 第三方云盘）由 storage_backends 表配置；后端
//     内的定位符是 object_ref，调用方不再接触物理路径。
//   - 分片对象由 blob_parts 描述，读取端实现跨片 ReadSeeker，Range 请求（预览、
//     断点续传）在分片与后续加密场景下同样可用。
//   - 加密元数据（DEK 用 .env 中的 KEK 包裹）保存在 storage_blobs；明文密钥只在
//     内存中出现。
package blobstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrBlobNotFound       = errors.New("blobstore: 对象不存在")
	ErrUnsafeRef          = errors.New("blobstore: 非法对象定位符")
	ErrChecksumMismatch   = errors.New("blobstore: 对象校验失败")
	ErrBackendUnavailable = errors.New("blobstore: 存储后端不可用")
	ErrNoDefaultBackend   = errors.New("blobstore: 未配置默认存储后端")
	// ErrBackendDisabled 表示目标后端存在且已加载，但被管理员停用：禁止写入。
	// 存量对象的读取、迁移与删除不受影响。
	ErrBackendDisabled = errors.New("blobstore: 目标存储后端已停用")
	ErrNotSupported    = errors.New("blobstore: 该存储后端不支持此操作")
)

// EncryptionMeta 描述一个对象的加密信息。DEK 是使用 .env 中 KEK 包裹后的密文；
// 明文算法为 AES-256-GCM 分块（S3 启用，S2 中恒为 nil）。
type EncryptionMeta struct {
	Algo  string
	KeyID string
	Nonce []byte
	DEK   []byte
}

// Blob 是 storage_blobs 中的物理对象记录。
type Blob struct {
	ID         string
	SizeBytes  int64
	SHA256     string
	BackendID  int64
	ObjectRef  string
	Encryption *EncryptionMeta
	ChunkSize  int32
	PartCount  int32
	RefCount   int64
	Status     string
}

// Part 是分片对象的单片元数据。
type Part struct {
	Index     int32
	SizeBytes int64
	ObjectRef string
}

// Backend 是物理存储后端统一接口。所有定位符都是后端内部标识，调用方不得假设
// 它是文件路径。
type Backend interface {
	Kind() string
	// Put 写入一个对象。ref 是调用方偏好的逻辑名（如 blobID），返回值为写入
	// 字节数与后端真实定位符——两者可能不同（例如 123 云盘用数字 fileID 定位，
	// 逻辑名只作为上传时的文件名）。真实定位符会被写入数据库，后续 Open/
	// Delete/Stat 必须使用它。
	Put(ctx context.Context, ref string, r io.Reader) (written int64, storedRef string, err error)
	Open(ctx context.Context, ref string) (io.ReadSeekCloser, error)
	Delete(ctx context.Context, ref string) error
	Stat(ctx context.Context, ref string) (int64, error)
}

// PresignPurpose 说明"这次直链给谁用"。
//
// 123 云盘的「直链流量」与「自用下载流量」是两份独立额度，管理员可以按用途分别
// 指定优先消耗哪一份，因此签名前必须告知用途（其它后端无此区分，忽略该参数）。
type PresignPurpose string

const (
	// PresignForShare 对外交付：分享页下载与文件页下载。
	PresignForShare PresignPurpose = "share"
	// PresignForDirect 站内直链 /d/<token>。
	PresignForDirect PresignPurpose = "direct"
)

// PurposeAwareOpener 由支持「按用途选择读取通道」的后端实现。
//
// 123 云盘的「自用下载流量」（download_info）与「直链流量」（direct-link）是两份
// 独立额度，两条通道取到的是同一份字节，只是消耗不同额度。交付层在读取对象时按
// 用途透传，由后端决定走哪条通道；不理解用途的后端保持默认行为。
type PurposeAwareOpener interface {
	OpenWithPurpose(ctx context.Context, ref string, purpose PresignPurpose) (io.ReadSeekCloser, error)
}

// Presigner 由支持「302 直跳」的后端实现（本机后端不实现，返回 ok=false 由
// 交付层降级为本机中转）。
type Presigner interface {
	// Presign 返回对象的第三方直链地址。ok=false 表示该对象不支持直链
	// （例如直链空间未启用，或管理员为该用途选择了自用下载流量），
	// 调用方应降级为中转交付。
	Presign(ctx context.Context, ref string, ttl time.Duration, purpose PresignPurpose) (url string, ok bool, err error)
}

// BatchDeleter 由支持「一次删除多个对象」的后端实现（如 123 云盘的回收站接口
// 单次可传 100 个 ID）。多分片对象逐片删除会放大远端 API 调用次数并可能触发
// 平台限流，批量删除把调用量降低到百次级。
type BatchDeleter interface {
	// DeleteBatch 删除一批对象；单个对象不存在不应视为错误（幂等）。
	DeleteBatch(ctx context.Context, refs []string) error
}

// Options 描述创建对象时的存储选择。BackendID 为 0 表示使用默认后端。
type Options struct {
	BackendID int64
	ChunkSize int32
	// Encrypt 为真时使用分块 AES-256-GCM 加密存储（需要配置 KEK）。
	Encrypt bool
}

// ServiceOptions 汇总存储服务所需的部署侧配置与凭据（全部来自 .env）。
type ServiceOptions struct {
	Encryption *EncryptionConfig
	Pan123     Pan123Credentials
	S3         S3Credentials
}

// backendEntry 是一个已成功加载的后端及其启用状态。
//
// 两者放在同一条记录里：「是否加载成功」与「是否启用」是判断"能否读取"与
// "能否写入"的全部依据，拆成两个需要同步的集合会留下构造上不一致的空档。
// 未加载的后端（配置不完整/未实现）根本不在 map 中。
type backendEntry struct {
	backend Backend
	// enabled 为 false 表示管理员已停用：该后端仍可读（存量对象的读取、迁移、
	// 删除），但禁止作为写入目标——默认解析与显式指定都会拒绝。
	enabled bool
}

// Service 组合数据库元数据与物理后端。
type Service struct {
	pool       *pgxpool.Pool
	root       RootProvider
	encryption *EncryptionConfig
	pan123     Pan123Credentials
	s3         S3Credentials

	mu        sync.RWMutex
	backends  map[int64]backendEntry
	defaultID int64
}

func NewService(pool *pgxpool.Pool, root RootProvider, options ServiceOptions) *Service {
	return &Service{
		pool: pool, root: root,
		encryption: options.Encryption, pan123: options.Pan123, s3: options.S3,
		backends: map[int64]backendEntry{},
	}
}

// EncryptionAvailable 表示当前部署是否配置了加密密钥（可用于加密新文件）。
func (s *Service) EncryptionAvailable() bool { return s.encryption.Available() }

// DefaultBackendAvailable 表示当前是否存在可用的默认存储后端（新上传的写入
// 目标）。管理端保证保存时至少保留一个「启用 + 默认」的后端，这里返回 false
// 对应绕过管理端的异常状态（上传应在初始化阶段据此明确拒绝）。
func (s *Service) DefaultBackendAvailable() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.defaultID != 0
}

// Presign 返回对象的第三方直链（若其后端支持且已启用直链）。ok=false 表示
// 该后端/对象不支持 302，交付层应降级为本机中转。
// 分片对象不参与 302：单个直链只能表达一个物理对象，否则会静默截断。
func (s *Service) Presign(ctx context.Context, blob Blob, ttl time.Duration, purpose PresignPurpose) (string, bool, error) {
	if blob.PartCount > 1 {
		return "", false, nil
	}
	backend, err := s.backend(blob.BackendID)
	if err != nil {
		return "", false, err
	}
	presigner, ok := backend.(Presigner)
	if !ok {
		return "", false, nil
	}
	return presigner.Presign(ctx, blob.ObjectRef, ttl, purpose)
}

// LoadedBackends 返回当前已成功加载的后端 ID 集合（管理界面用于展示
// "已就绪/未就绪"：例如 123 云盘缺少凭据或根目录配置时不会被加载）。
func (s *Service) LoadedBackends() map[int64]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[int64]bool, len(s.backends))
	for id := range s.backends {
		out[id] = true
	}
	return out
}

// BackendEnabled 报告后端当前是否可用于写入：已成功加载且未被停用。
// 未加载的 id 得到零值条目的 enabled=false，天然等价于「不可写入」。
func (s *Service) BackendEnabled(id int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.backends[id].enabled
}

// Pan123Configured 表示部署是否配置了 123 云盘应用凭据。
func (s *Service) Pan123Configured() bool {
	return strings.TrimSpace(s.pan123.ClientID) != "" && strings.TrimSpace(s.pan123.ClientSecret) != ""
}

// S3Configured 表示部署是否配置了对象存储访问凭据。
func (s *Service) S3Configured() bool {
	return strings.TrimSpace(s.s3.AccessKeyID) != "" && strings.TrimSpace(s.s3.SecretAccessKey) != ""
}

// Reload 从 storage_backends 重新加载后端配置。启动时必须调用一次；后端配置
// 变更（S4 管理员界面）后由调用方再次调用。无法识别的后端类型会被跳过并记录
// 日志，不影响已支持的后端继续工作。
//
// 停用的后端同样会被加载：启用状态只影响"能否作为写入目标"（默认解析与显式
// 指定），不影响存量对象的读取、迁移与删除——管理员的"停用"意图是停止新增
// 写入，而不是让历史文件失联。
func (s *Service) Reload(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `
		SELECT id, kind, is_default, is_enabled, settings FROM storage_backends
		ORDER BY is_default DESC, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	backends := make(map[int64]backendEntry)
	defaultID := int64(0)
	for rows.Next() {
		var id int64
		var kind string
		var isDefault, isEnabled bool
		var rawSettings []byte
		if err := rows.Scan(&id, &kind, &isDefault, &isEnabled, &rawSettings); err != nil {
			return err
		}
		var backend Backend
		switch kind {
		case "local":
			backend = NewLocalBackend(s.root)
		case "pan123":
			pan123 := s.newPan123Backend(rawSettings)
			if err := pan123.Configured(); err != nil {
				log.Printf("blobstore: 存储后端 %d（123 云盘）配置不完整，已跳过：%v", id, err)
				continue
			}
			// 直链空间只对启用后端准备：停用后端不参与交付，也不应改写远端状态。
			if isEnabled && pan123.NeedDirectLink() {
				enableCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				if err := pan123.EnableDirectLink(enableCtx); err != nil {
					// 直链失败不影响中转交付，仅记录。
					log.Printf("blobstore: 存储后端 %d 启用 123 云盘直链空间失败：%v", id, err)
				}
				cancel()
			}
			backend = pan123
		case "s3":
			s3 := s.newS3Backend(rawSettings)
			if err := s3.Configured(); err != nil {
				log.Printf("blobstore: 存储后端 %d（对象存储）配置不完整，已跳过：%v", id, err)
				continue
			}
			backend = s3
		default:
			// 未实现的后端跳过：引用它的对象读取时返回"存储后端不可用"，
			// 不会静默降级到其它后端。
			log.Printf("blobstore: 存储后端 %d 类型 %s 尚未实现，已跳过", id, kind)
			continue
		}
		backends[id] = backendEntry{backend: backend, enabled: isEnabled}
		// 默认后端必须"已启用且加载成功"：停用或配置不完整的后端不能承接新写入。
		if isEnabled && isDefault && defaultID == 0 {
			defaultID = id
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.backends = backends
	s.defaultID = defaultID
	s.mu.Unlock()
	return nil
}

// newPan123Backend 从 storage_backends.settings 构造 123 云盘后端。
// settings 承载根目录 ID、是否启用直链与直链鉴权密钥；开放平台应用凭据来自 .env。
// 鉴权密钥只用于服务端签名，读接口不会把它回传浏览器。
func (s *Service) newPan123Backend(rawSettings []byte) *Pan123Backend {
	var settings struct {
		ParentFileID int64 `json:"parent_file_id"`
		// 历史配置里的 direct_link（启用直链总闸）已废弃：是否需要直链由交付方式与
		// 额度偏好推导（NeedDirectLink），此处不再解析。
		DirectLinkAuth    bool   `json:"direct_link_auth"`
		DirectLinkAuthKey string `json:"direct_link_auth_key"`
		// 额度偏好（仅本机中转时生效）：share_prefer 用于分享页/文件页下载，
		// direct_prefer 用于站内直链；取值 download（自用下载流量，默认）或
		// direct（直链流量）。交付方式 delivery_prefer：proxy（优先本机中转，默认）
		// 或 redirect（优先 302 直连第三方）。
		SharePrefer    string `json:"share_prefer"`
		DirectPrefer   string `json:"direct_prefer"`
		DeliveryPrefer string `json:"delivery_prefer"`
		// ChannelSwitch 为 nil 时视为关闭（严格使用所选通道，异常不切换）。
		ChannelSwitch *bool `json:"channel_switch"`
	}
	if len(rawSettings) > 0 {
		if err := json.Unmarshal(rawSettings, &settings); err != nil {
			log.Printf("blobstore: 解析 123 云盘后端配置失败：%v", err)
		}
	}
	// 缺省关闭：严格使用所选通道（"优先"即"始终"），异常时不消耗另一份额度。
	channelSwitch := false
	if settings.ChannelSwitch != nil {
		channelSwitch = *settings.ChannelSwitch
	}
	return NewPan123Backend(Pan123Config{
		ClientID:          s.pan123.ClientID,
		ClientSecret:      s.pan123.ClientSecret,
		ParentFileID:      settings.ParentFileID,
		DirectLinkAuth:    settings.DirectLinkAuth,
		DirectLinkAuthKey: settings.DirectLinkAuthKey,
		SharePrefer:       settings.SharePrefer,
		DirectPrefer:      settings.DirectPrefer,
		DeliveryPrefer:    settings.DeliveryPrefer,
		ChannelSwitch:     channelSwitch,
	})
}

// newS3Backend 从 storage_backends.settings 构造 S3 兼容对象存储后端。
func (s *Service) newS3Backend(rawSettings []byte) *S3Backend {
	var settings struct {
		Endpoint  string `json:"endpoint"`
		Region    string `json:"region"`
		Bucket    string `json:"bucket"`
		Prefix    string `json:"prefix"`
		PathStyle *bool  `json:"path_style"`
	}
	if len(rawSettings) > 0 {
		if err := json.Unmarshal(rawSettings, &settings); err != nil {
			log.Printf("blobstore: 解析对象存储后端配置失败：%v", err)
		}
	}
	// 默认 path-style 寻址（MinIO 等自建服务更常见；AWS 新桶需关闭）。
	pathStyle := true
	if settings.PathStyle != nil {
		pathStyle = *settings.PathStyle
	}
	return NewS3Backend(S3Config{
		Endpoint:    settings.Endpoint,
		Region:      settings.Region,
		Bucket:      settings.Bucket,
		Prefix:      settings.Prefix,
		PathStyle:   pathStyle,
		Credentials: s.s3,
	})
}

func (s *Service) backend(id int64) (Backend, error) {
	s.mu.RLock()
	entry, ok := s.backends[id]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: backend=%d", ErrBackendUnavailable, id)
	}
	return entry.backend, nil
}

func (s *Service) resolveBackend(requested int64) (Backend, int64, error) {
	if requested == 0 {
		s.mu.RLock()
		requested = s.defaultID
		s.mu.RUnlock()
	}
	if requested == 0 {
		// 没有默认后端（未配置 / 被停用 / 配置不完整）时明确失败：管理端保存时
		// 强制保留一个启用的默认后端，该错误只对应绕过管理端的异常状态。不做
		// 静默回退——遍历 map 选择"任一后端"会让数据写入次序不确定的位置。
		return nil, 0, ErrNoDefaultBackend
	}
	backend, err := s.backend(requested)
	if err != nil {
		return nil, 0, err
	}
	s.mu.RLock()
	enabled := s.backends[requested].enabled
	s.mu.RUnlock()
	if !enabled {
		// 后端存在但已停用：拒绝写入。读取路径（Open / Delete / Presign 等）
		// 不经过这里——停用后端的存量对象仍可读、可删除、可迁移。
		return nil, 0, fmt.Errorf("%w: backend=%d", ErrBackendDisabled, requested)
	}
	return backend, requested, nil
}

// Create 把 src 写入为一个新的物理对象。
//
//   - sizeBytes 为明文总大小；写入不足或超出都会失败并清理已写入的对象。
//   - expectedSHA 非空时逐字节计算 SHA-256 并比对（流式，无额外 IO；
//     加密场景下哈希始终基于明文）。
//   - opts.ChunkSize > 0 时按片写入（片大小按明文记，必须是加密块的整数倍），
//     片定位符为 "<blobID>.p<index>"。
//   - opts.Encrypt 为真时对内容做分块 AES-256-GCM 加密，每文件随机 DEK 用
//     KEK 包裹后随对象元数据保存。
//
// 返回的对象尚未被任何资源引用（ref_count=0），调用方需在同一事务中 Attach。
func (s *Service) Create(ctx context.Context, src io.Reader, sizeBytes int64, expectedSHA string, opts Options) (Blob, error) {
	backend, backendID, err := s.resolveBackend(opts.BackendID)
	if err != nil {
		return Blob{}, err
	}
	if sizeBytes < 0 {
		return Blob{}, fmt.Errorf("blobstore: 大小无效")
	}
	blobID, err := randomtoken.New(18)
	if err != nil {
		return Blob{}, err
	}
	hash := sha256.New()
	var reader io.Reader = io.TeeReader(src, hash)

	var encryption *EncryptionMeta
	cryptoChunk := int64(0)
	if opts.Encrypt {
		meta, encryptedReader, encErr := s.newEncryption(reader)
		if encErr != nil {
			return Blob{}, encErr
		}
		encryption = meta
		reader = encryptedReader
		cryptoChunk = EncryptionChunkSize
	}

	cleanup := func(refs []string) {
		// 清理必须与请求上下文解耦：客户端取消/超时不应让"半成品对象"失去清理
		// 机会（否则会在存储后端留下无记录的垃圾）。
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		for _, ref := range refs {
			if err := backend.Delete(cleanupCtx, ref); err != nil {
				log.Printf("blobstore: 清理未完成对象 %s 失败: %v", ref, err)
			}
		}
	}

	written, err := s.writeObject(ctx, backend, blobID, reader, sizeBytes, int64(opts.ChunkSize), cryptoChunk)
	if err != nil {
		cleanup(written.refs)
		return Blob{}, err
	}

	sum := hex.EncodeToString(hash.Sum(nil))
	if expectedSHA != "" && sum != expectedSHA {
		cleanup(written.refs)
		return Blob{}, fmt.Errorf("%w: sha256 不一致", ErrChecksumMismatch)
	}

	blob := Blob{
		ID: blobID, SizeBytes: sizeBytes, SHA256: sum, BackendID: backendID,
		ObjectRef: written.objectRef, ChunkSize: opts.ChunkSize,
		PartCount: int32(len(written.parts)), Status: "active",
		Encryption: encryption,
	}
	if len(written.parts) == 0 {
		blob.PartCount = 1
	}
	if err := s.insertObject(ctx, blob, written); err != nil {
		cleanup(written.refs)
		return Blob{}, err
	}
	return blob, nil
}

// insertObject 写入对象及其分片元数据（独立事务）。
func (s *Service) insertObject(ctx context.Context, blob Blob, written writtenObject) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	var algo, keyID any
	var nonce, dek any
	if blob.Encryption != nil {
		algo, keyID, nonce, dek = blob.Encryption.Algo, blob.Encryption.KeyID,
			blob.Encryption.Nonce, blob.Encryption.DEK
	}
	// 新对象先以 orphaned 落库：只有被资源引用（Attach 置为 active）后才算"在用"。
	// 这样上传中途失败、维护任务替换失败等场景留下的残行天然可被孤儿清理任务
	// 扫到（清理任务另有 1 小时宽限期，避免与正在进行的上传竞争）。
	if _, err := tx.Exec(ctx, `
		INSERT INTO storage_blobs (
			id, size_bytes, sha256, backend_id, object_ref, chunk_size, part_count, ref_count, status,
			encryption_algo, encryption_key_id, encryption_nonce, encryption_dek
		) VALUES ($1,$2,$3,$4,$5,$6,$7,0,'orphaned',$8,$9,$10,$11)`,
		blob.ID, blob.SizeBytes, blob.SHA256, blob.BackendID, blob.ObjectRef,
		blob.ChunkSize, blob.PartCount, algo, keyID, nonce, dek); err != nil {
		return err
	}
	for _, part := range written.parts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO blob_parts (blob_id, part_index, size_bytes, object_ref)
			VALUES ($1,$2,$3,$4)`, blob.ID, part.Index, part.SizeBytes, part.ObjectRef); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// newEncryption 生成每文件随机 DEK 与文件级 nonce，用主 KEK 包裹 DEK，
// 并返回加密写入流。
func (s *Service) newEncryption(src io.Reader) (*EncryptionMeta, io.Reader, error) {
	if !s.encryption.Available() {
		return nil, nil, ErrEncryptionUnavailable
	}
	dek := make([]byte, dekSize)
	if _, err := rand.Read(dek); err != nil {
		return nil, nil, err
	}
	fileNonce := make([]byte, fileNonceSize)
	if _, err := rand.Read(fileNonce); err != nil {
		return nil, nil, err
	}
	keyID, envelope, err := s.encryption.wrapDEK(dek)
	if err != nil {
		return nil, nil, err
	}
	reader, err := newEncryptingReader(src, dek, fileNonce, EncryptionChunkSize)
	if err != nil {
		return nil, nil, err
	}
	return &EncryptionMeta{Algo: AlgoAESGCMChunked, KeyID: keyID, Nonce: fileNonce, DEK: envelope}, reader, nil
}

// RewriteObject 把已有对象按新的存储位置/加密策略重写一份，返回替换后的对象
// 元数据（不改数据库、不删旧对象——由调用方在同一流程里完成替换与清理）。
// 用于管理员维护任务：跨后端迁移、存量补加密。
func (s *Service) RewriteObject(ctx context.Context, src Blob, backendID int64, encrypt bool, chunkSize int32) (Blob, error) {
	reader, err := s.Open(ctx, src)
	if err != nil {
		return Blob{}, err
	}
	defer reader.Close()
	options := Options{BackendID: backendID, ChunkSize: chunkSize, Encrypt: encrypt}
	if encrypt && !s.encryption.Available() {
		return Blob{}, ErrEncryptionUnavailable
	}
	replacement, err := s.Create(ctx, reader, src.SizeBytes, src.SHA256, options)
	if err != nil {
		return Blob{}, err
	}
	return replacement, nil
}

// ReplaceObject 在事务内把对象的存储定位与加密元数据替换为 replacement（由
// RewriteObject 创建的新物理对象），保留对象 ID、引用计数与状态；新对象的临时
// 元数据行被丢弃，避免留下无人引用的中转对象。
func (s *Service) ReplaceObject(ctx context.Context, id string, replacement Blob) error {
	var parts []Part
	if replacement.PartCount > 1 {
		listed, err := s.listParts(ctx, replacement.ID)
		if err != nil {
			return err
		}
		parts = listed
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	var algo, keyID, nonce, dek any
	if replacement.Encryption != nil {
		algo, keyID, nonce, dek = replacement.Encryption.Algo, replacement.Encryption.KeyID,
			replacement.Encryption.Nonce, replacement.Encryption.DEK
	}
	tag, err := tx.Exec(ctx, `
		UPDATE storage_blobs SET
			backend_id=$2, object_ref=$3, chunk_size=$4, part_count=$5,
			size_bytes=$6, sha256=$7,
			encryption_algo=$8, encryption_key_id=$9, encryption_nonce=$10, encryption_dek=$11,
			updated_at=NOW()
		WHERE id=$1`,
		id, replacement.BackendID, replacement.ObjectRef, replacement.ChunkSize, replacement.PartCount,
		replacement.SizeBytes, replacement.SHA256,
		algo, keyID, nonce, dek)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBlobNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM blob_parts WHERE blob_id=$1`, id); err != nil {
		return err
	}
	for _, part := range parts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO blob_parts (blob_id, part_index, size_bytes, object_ref)
			VALUES ($1,$2,$3,$4)`, id, part.Index, part.SizeBytes, part.ObjectRef); err != nil {
			return err
		}
	}
	// 丢弃 RewriteObject 创建的临时对象行（分片行随外键级联删除）。
	if replacement.ID != id {
		dropTag, err := tx.Exec(ctx, `DELETE FROM storage_blobs WHERE id=$1`, replacement.ID)
		if err != nil {
			return err
		}
		if dropTag.RowsAffected() == 0 {
			return fmt.Errorf("blobstore: 临时对象行不存在（id=%s）", replacement.ID)
		}
	}
	return tx.Commit(ctx)
}

// ObjectRefs 返回对象在当前物理后端上的全部定位符（分片对象返回各分片定位符）。
//
// 必须在 ReplaceObject 之前取用：替换会重写 blob_parts 指向新对象，替换后按 ID
// 回查得到的是新定位符——拿它去旧后端删除会删错对象（同后端场景直接删掉新写入
// 的对象，跨后端场景留下永不回收的旧物理残留）。
func (s *Service) ObjectRefs(ctx context.Context, blob Blob) ([]string, error) {
	if blob.PartCount <= 1 {
		return []string{blob.ObjectRef}, nil
	}
	parts, err := s.listParts(ctx, blob.ID)
	if err != nil {
		// 拿不到分片清单时不能只删首片（会留下无法追溯的残余分片）。
		return nil, err
	}
	refs := make([]string, 0, len(parts))
	for _, part := range parts {
		refs = append(refs, part.ObjectRef)
	}
	return refs, nil
}

// DeletePhysicalRefs 在指定后端上删除一批物理对象（按定位符直接删除，不查库）。
// 支持批量删除的后端优先批量（多分片对象可把调用次数降低 100 倍）。
func (s *Service) DeletePhysicalRefs(ctx context.Context, backendID int64, refs []string) error {
	if len(refs) == 0 {
		return nil
	}
	backend, err := s.backend(backendID)
	if err != nil {
		return err
	}
	if batch, ok := backend.(BatchDeleter); ok {
		return batch.DeleteBatch(ctx, refs)
	}
	var firstErr error
	for _, ref := range refs {
		if err := backend.Delete(ctx, ref); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// DropObject 删除对象元数据（分片行级联删除），用于孤儿对象清理。
// 只允许删除零引用对象；被引用时返回未删除（调用方据此判定竞态）。
func (s *Service) DropObject(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM storage_blobs WHERE id=$1 AND ref_count <= 0`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

type writtenObject struct {
	objectRef string
	refs      []string
	parts     []Part
}

// writeObject 把数据流写入物理后端。sizeBytes 是明文总大小；cryptoChunk > 0
// 表示流已按该块大小分块加密（密文长度 = 明文 + 16×块数），写完后按密文
// 长度严格校验，防止流长度与声明不一致。
func (s *Service) writeObject(ctx context.Context, backend Backend, blobID string, reader io.Reader, sizeBytes, chunkSize, cryptoChunk int64) (writtenObject, error) {
	if sizeBytes < 0 {
		return writtenObject{}, fmt.Errorf("blobstore: 大小无效")
	}
	if chunkSize <= 0 || sizeBytes == 0 {
		// 单对象（空对象也走单对象：分片模式下零字节不会产生任何分片，
		// 若沿用分片分支其 object_ref 会落成逻辑名而非真实定位符）。
		// 限制读取 wire+1 以发现超长输入。
		wire := wireSize(sizeBytes, cryptoChunk)
		n, storedRef, err := backend.Put(ctx, blobID, io.LimitReader(reader, wire+1))
		if err != nil {
			return writtenObject{}, err
		}
		if n != wire {
			return writtenObject{refs: []string{storedRef}}, fmt.Errorf("blobstore: 写入字节数与声明大小不一致")
		}
		return writtenObject{objectRef: storedRef, refs: []string{storedRef}}, nil
	}
	if cryptoChunk > 0 && chunkSize%cryptoChunk != 0 {
		return writtenObject{}, ErrEncryptionPartAlign
	}

	result := writtenObject{}
	remaining := sizeBytes
	index := int32(0)
	for remaining > 0 {
		partSize := chunkSize
		if remaining < partSize {
			partSize = remaining
		}
		partWire := wireSize(partSize, cryptoChunk)
		// 逻辑名仅用于后端侧命名（如 123 云盘的上传文件名），真实定位符以后端
		// 返回为准（可能是数字 fileID）。
		logicalRef := fmt.Sprintf("%s.p%d", blobID, index)
		// 分片场景不能"多读 1 字节"探测超长输入：后续分片的数据就在同一个流里，
		// 多读必然命中，因此这里严格读取本片应有的字节数。
		n, storedRef, err := backend.Put(ctx, logicalRef, io.LimitReader(reader, partWire))
		if err != nil {
			if storedRef != "" {
				result.refs = append(result.refs, storedRef)
			}
			return result, err
		}
		if n != partWire {
			result.refs = append(result.refs, storedRef)
			return result, fmt.Errorf("blobstore: 分片写入不完整")
		}
		result.refs = append(result.refs, storedRef)
		result.parts = append(result.parts, Part{Index: index, SizeBytes: partSize, ObjectRef: storedRef})
		if result.objectRef == "" {
			result.objectRef = storedRef
		}
		remaining -= partSize
		index++
	}
	if result.objectRef == "" {
		result.objectRef = blobID
	}
	return result, nil
}

// Get 读取对象元数据。
func (s *Service) Get(ctx context.Context, id string) (Blob, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, size_bytes, sha256, backend_id, object_ref,
		       encryption_algo, encryption_key_id, encryption_nonce, encryption_dek,
		       chunk_size, part_count, ref_count, status
		FROM storage_blobs WHERE id = $1`, id)
	blob, err := scanBlob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Blob{}, ErrBlobNotFound
	}
	return blob, err
}

// Open 打开对象内容（明文）：加密对象返回按块解密的 ReadSeeker，未加密对象
// 直接返回后端流（分片对象返回跨片 ReadSeeker）。Seek 按明文偏移工作，
// Range/预览/断点续传在加密与分片场景下同样可用。
//
// 内部读取（迁移、校验等）走默认通道；对外交付请用 OpenWithPurpose 指定用途，
// 让支持双通道的后端（123 云盘）按用途消耗对应额度。
func (s *Service) Open(ctx context.Context, blob Blob) (io.ReadSeekCloser, error) {
	return s.OpenWithPurpose(ctx, blob, "")
}

// OpenWithPurpose 与 Open 相同，但把「用途」透传给支持按用途选通道的后端：
// 123 云盘的直链流量（direct-link）与自用下载流量（download_info）是两份额度，
// 交付层据此选择消耗哪一份，取到的字节完全相同。
func (s *Service) OpenWithPurpose(ctx context.Context, blob Blob, purpose PresignPurpose) (io.ReadSeekCloser, error) {
	reader, err := s.OpenRawWithPurpose(ctx, blob, purpose)
	if err != nil {
		return nil, err
	}
	if blob.Encryption == nil {
		return reader, nil
	}
	if !s.encryption.Available() {
		_ = reader.Close()
		return nil, ErrEncryptionUnavailable
	}
	dek, err := s.encryption.unwrapDEK(blob.Encryption.KeyID, blob.Encryption.DEK)
	if err != nil {
		_ = reader.Close()
		return nil, err
	}
	return newEncryptedReadSeeker(reader, dek, blob.Encryption.Nonce, blob.SizeBytes, EncryptionChunkSize)
}

// OpenRaw 打开对象的原始（密文）内容：单对象直接返回后端流，分片对象返回
// 跨片 ReadSeeker。用于"服务器不代理解密"的交付（由浏览器端解密）。
func (s *Service) OpenRaw(ctx context.Context, blob Blob) (io.ReadSeekCloser, error) {
	return s.OpenRawWithPurpose(ctx, blob, "")
}

// OpenRawWithPurpose 与 OpenRaw 相同，但按用途选择后端读取通道。
func (s *Service) OpenRawWithPurpose(ctx context.Context, blob Blob, purpose PresignPurpose) (io.ReadSeekCloser, error) {
	backend, err := s.backend(blob.BackendID)
	if err != nil {
		return nil, err
	}
	if blob.PartCount <= 1 {
		return openRef(ctx, backend, blob.ObjectRef, purpose)
	}
	parts, err := s.listParts(ctx, blob.ID)
	if err != nil {
		return nil, err
	}
	cryptoChunk := int64(0)
	if blob.Encryption != nil {
		cryptoChunk = EncryptionChunkSize
	}
	return newPartsReader(ctx, backend, parts, cryptoChunk, purpose), nil
}

// openRef 打开后端的物理对象；后端支持按用途选通道且指定了用途时透传（123 云盘），
// 否则走后端默认通道。
func openRef(ctx context.Context, backend Backend, ref string, purpose PresignPurpose) (io.ReadSeekCloser, error) {
	if purpose != "" {
		if opener, ok := backend.(PurposeAwareOpener); ok {
			return opener.OpenWithPurpose(ctx, ref, purpose)
		}
	}
	return backend.Open(ctx, ref)
}

// WireSize 返回对象在存储层的字节数（加密对象含 tag 开销）。
func (blob Blob) WireSize() int64 {
	if blob.Encryption == nil {
		return blob.SizeBytes
	}
	return wireSize(blob.SizeBytes, EncryptionChunkSize)
}

// DecryptDEK 取出并用 KEK 解开对象的 DEK。仅用于"密钥下发给浏览器端解密"
// 的交付路径；调用方不得记录或返回明文密钥。
func (s *Service) DecryptDEK(blob Blob) ([]byte, error) {
	if blob.Encryption == nil {
		return nil, fmt.Errorf("blobstore: 对象未加密")
	}
	if !s.encryption.Available() {
		return nil, ErrEncryptionUnavailable
	}
	return s.encryption.unwrapDEK(blob.Encryption.KeyID, blob.Encryption.DEK)
}

func (s *Service) listParts(ctx context.Context, blobID string) ([]Part, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT part_index, size_bytes, object_ref FROM blob_parts
		WHERE blob_id = $1 ORDER BY part_index`, blobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parts := make([]Part, 0)
	for rows.Next() {
		var part Part
		if err := rows.Scan(&part.Index, &part.SizeBytes, &part.ObjectRef); err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, ErrBlobNotFound
	}
	return parts, nil
}

// Attach 增加引用计数，必须在保存资源的事务内调用。
func (s *Service) Attach(ctx context.Context, tx pgx.Tx, id string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE storage_blobs SET ref_count = ref_count + 1, status = 'active', updated_at = NOW()
		WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBlobNotFound
	}
	return nil
}

// ReleaseInTx 在事务内减少引用计数；归零时标记 orphaned（物理文件保留，
// 由管理员清理工具处理）。
// releaseBlobSQL 把一个对象的引用计数减一；减到 0 时标记为孤儿（等待清理
// 任务或后续复用）。Release 与 ReleaseInTx 共用，避免两份 SQL 漂移。
const releaseBlobSQL = `
	UPDATE storage_blobs SET
		ref_count = GREATEST(ref_count - 1, 0),
		status = CASE WHEN ref_count - 1 <= 0 THEN 'orphaned' ELSE status END,
		updated_at = NOW()
	WHERE id = $1`

func (s *Service) ReleaseInTx(ctx context.Context, tx pgx.Tx, id string) error {
	if id == "" {
		return nil
	}
	_, err := tx.Exec(ctx, releaseBlobSQL, id)
	return err
}

// FindReusable 查找可复用的对象（跨用户秒传）：同内容哈希、仍活跃，
// 且没有任何被管理员限制（拉黑/处置删除）的资源引用它——违规内容不得借
// 秒传复活。encrypted 表示当前上传策略：加密策略只复用加密对象，明文策略
// 只复用明文对象，避免"开启加密后新文件仍然以明文落盘"。
func (s *Service) FindReusable(ctx context.Context, sha256Sum string, sizeBytes int64, encrypted bool, ownerID int64, allowCrossUser bool) (Blob, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT b.id, b.size_bytes, b.sha256, b.backend_id, b.object_ref,
		       b.encryption_algo, b.encryption_key_id, b.encryption_nonce, b.encryption_dek,
		       b.chunk_size, b.part_count, b.ref_count, b.status
		FROM storage_blobs b
		WHERE b.sha256 = $1 AND b.size_bytes = $2 AND b.status = 'active'
		  AND (b.encryption_algo IS NOT NULL) = $3
		  -- 复用范围：allowCrossUser 为真时按内容全平台去重（省存储）；为假时只
		  -- 复用"请求者自己已有引用"的对象，避免他人凭已知哈希+大小领取私有内容。
		  AND ($4 OR EXISTS (
		      SELECT 1 FROM resources r4 WHERE r4.blob_id = b.id AND r4.owner_user_id = $5
		  ))
		  -- 被限制的内容不可复用：引用资源被拉黑/受限，或审核处于待审/驳回
		  -- （审核通过或从未进入审核才允许复用，避免违规内容借秒传扩散）。
		  AND NOT EXISTS (
		      SELECT 1 FROM resources r
		      LEFT JOIN file_moderations m ON m.resource_id = r.id
		      WHERE r.blob_id = b.id
		        AND (r.admin_blocked OR r.restore_blocked OR m.status IN ('pending', 'rejected'))
		  )
		ORDER BY b.created_at DESC LIMIT 1`, sha256Sum, sizeBytes, encrypted, allowCrossUser, ownerID)
	blob, err := scanBlob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Blob{}, ErrBlobNotFound
	}
	return blob, err
}

// VerifyAvailable 低成本检查对象在后端仍然存在（Stat，不读取内容），用于
// 秒传复用与交付前的可用性确认。加密对象的物理大小包含 tag 开销。
//
// 分片对象采用抽样校验：远端后端（123 云盘 / 对象存储）每片 Stat 都是一次
// API 调用，逐片校验会让大文件的交付准备产生上百次请求（显著延迟并可能触发
// 平台限流）。分片数 ≤4 时全查；更多时抽查首、中、末三片——对象整体丢失或
// 大范围损坏仍会被发现，单片偶发缺失由交付读取时的流错误承接（下载中断并
// 报错，不会输出错误数据）。
func (s *Service) VerifyAvailable(ctx context.Context, blob Blob) error {
	backend, err := s.backend(blob.BackendID)
	if err != nil {
		return err
	}
	cryptoChunk := int64(0)
	if blob.Encryption != nil {
		cryptoChunk = EncryptionChunkSize
	}
	if blob.PartCount <= 1 {
		size, err := backend.Stat(ctx, blob.ObjectRef)
		if err != nil {
			return err
		}
		if size != wireSize(blob.SizeBytes, cryptoChunk) {
			return fmt.Errorf("%w: 大小不一致", ErrChecksumMismatch)
		}
		return nil
	}
	parts, err := s.listParts(ctx, blob.ID)
	if err != nil {
		return err
	}
	// 本地一致性校验（零额外成本）：分片行数须与元数据一致、各片大小之和须等于
	// 对象总长——分片行丢失/错位在这里就能发现，不必依赖远端抽样。
	if int32(len(parts)) != blob.PartCount {
		return fmt.Errorf("%w: 分片数量不一致（元数据 %d，实际 %d）",
			ErrChecksumMismatch, blob.PartCount, len(parts))
	}
	var partsTotal int64
	for _, part := range parts {
		partsTotal += part.SizeBytes
	}
	if partsTotal != blob.SizeBytes {
		return fmt.Errorf("%w: 分片大小总和与对象长度不一致", ErrChecksumMismatch)
	}
	indexes := make([]int, 0, len(parts))
	if len(parts) <= 8 {
		for index := range parts {
			indexes = append(indexes, index)
		}
	} else {
		// 远端后端逐片 Stat 的成本随片数增长（123 云盘每次都是一次 API 调用），
		// 因此大对象抽样：均匀取 5 个点（替代固定首/中/末），提高发现缺失分片的
		// 概率；小对象（≤8 片）全量校验。
		last := len(parts) - 1
		indexes = append(indexes, 0, last/4, last/2, last*3/4, last)
	}
	checked := make(map[int]struct{}, len(indexes))
	for _, index := range indexes {
		if _, exists := checked[index]; exists {
			continue
		}
		checked[index] = struct{}{}
		part := parts[index]
		size, err := backend.Stat(ctx, part.ObjectRef)
		if err != nil {
			return err
		}
		if size != wireSize(part.SizeBytes, cryptoChunk) {
			return fmt.Errorf("%w: 分片大小不一致", ErrChecksumMismatch)
		}
	}
	return nil
}

// Release 在事务外减少引用计数（归零标记 orphaned）；失败清理等场景使用。
func (s *Service) Release(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, releaseBlobSQL, id)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanBlob(row rowScanner) (Blob, error) {
	var blob Blob
	var algo, keyID *string
	var nonce, dek []byte
	if err := row.Scan(&blob.ID, &blob.SizeBytes, &blob.SHA256, &blob.BackendID, &blob.ObjectRef,
		&algo, &keyID, &nonce, &dek, &blob.ChunkSize, &blob.PartCount, &blob.RefCount, &blob.Status); err != nil {
		return Blob{}, err
	}
	if algo != nil && keyID != nil {
		blob.Encryption = &EncryptionMeta{Algo: *algo, KeyID: *keyID, Nonce: nonce, DEK: dek}
	}
	return blob, nil
}

// partsReader 把分片对象组合成支持 Seek 的连续流，供 http.ServeContent 等
// 需要 io.ReadSeeker 的调用方使用（Range 请求会跨片正确读取）。
// 坐标系是"物理（存储层）字节"：加密分片的实际长度 = 明文片长 + tag 开销，
// cryptoChunk 为 0（未加密）时两者一致。
type partsReader struct {
	ctx     context.Context
	backend Backend
	// purpose 是该次交付的用途（空值=后端默认通道），逐片打开时同样透传，
	// 保证一次交付内所有分片走同一份额度。
	purpose PresignPurpose
	parts   []Part
	starts  []int64
	lengths []int64
	total   int64
	pos     int64
	current int
	file    io.ReadSeekCloser
	// filePos 是底层流当前的真实位置（物理坐标）；-1 表示尚未打开。Seek 可
	// 跳转到任意位置（含同一片内的跳转），读取前必须据此校准底层流，否则会
	// 从旧位置继续读出错位数据。
	filePos int64
}

func newPartsReader(ctx context.Context, backend Backend, parts []Part, cryptoChunk int64, purpose PresignPurpose) *partsReader {
	starts := make([]int64, len(parts))
	lengths := make([]int64, len(parts))
	var total int64
	for index, part := range parts {
		starts[index] = total
		lengths[index] = wireSize(part.SizeBytes, cryptoChunk)
		total += lengths[index]
	}
	return &partsReader{
		ctx: ctx, backend: backend, parts: parts, purpose: purpose,
		starts: starts, lengths: lengths, total: total, current: -1, filePos: -1,
	}
}

func (r *partsReader) partIndexFor(pos int64) int {
	index := 0
	for index+1 < len(r.parts) && pos >= r.starts[index+1] {
		index++
	}
	return index
}

func (r *partsReader) Read(p []byte) (int, error) {
	if r.pos >= r.total {
		return 0, io.EOF
	}
	index := r.partIndexFor(r.pos)
	if r.current != index {
		if err := r.openPart(index); err != nil {
			return 0, err
		}
	}
	// 校准底层流到逻辑位置：Seek 可跳转到任意位置（首次进入某片或同片内
	// 跳转都会出现），不校准会从旧位置返回错位数据。
	if r.filePos != r.pos {
		if _, err := r.file.Seek(r.pos-r.starts[index], io.SeekStart); err != nil {
			return 0, err
		}
		r.filePos = r.pos
	}
	remaining := r.starts[index] + r.lengths[index] - r.pos
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.file.Read(p)
	r.pos += int64(n)
	r.filePos = r.pos
	if err == io.EOF && r.pos < r.total {
		// 单片提前结束：整体并未读完，不能把截断伪装成正常结束。
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

// seekTarget 统一实现三种 whence 的定位计算，并拒绝负位置。
// size 是对象总长度（SeekEnd 的基准）。三个 ReadSeeker（分片、加密、远端）
// 共用，避免同一段 switch 三次拷贝后行为漂移。
func seekTarget(pos, size, offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = pos + offset
	case io.SeekEnd:
		target = size + offset
	default:
		return 0, fmt.Errorf("blobstore: 不支持的 Seek 模式")
	}
	if target < 0 {
		return 0, fmt.Errorf("blobstore: Seek 位置无效")
	}
	return target, nil
}

func (r *partsReader) Seek(offset int64, whence int) (int64, error) {
	target, err := seekTarget(r.pos, r.total, offset, whence)
	if err != nil {
		return 0, err
	}
	r.pos = target
	return target, nil
}

func (r *partsReader) openPart(index int) error {
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	file, err := openRef(r.ctx, r.backend, r.parts[index].ObjectRef, r.purpose)
	if err != nil {
		return err
	}
	r.file = file
	r.current = index
	// 新打开的流位置在片首（物理坐标 starts[index]），片内偏移由读取前的
	// 校准逻辑负责。
	r.filePos = r.starts[index]
	return nil
}

func (r *partsReader) Close() error {
	if r.file != nil {
		err := r.file.Close()
		r.file = nil
		return err
	}
	return nil
}
