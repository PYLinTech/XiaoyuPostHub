package server

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
)

// 统一交付接口：把"已鉴权的这一次下载"从业务地址中解耦，使用进程内存中的
// 随机地址 /dl/<id> 交付。
//
//   - 会话 ID 为 24 字节随机值（base64url 编码后 32 字符），不落库、不包含
//     资源或业务标识，重启即全部失效；
//   - 业务侧（分享下载任务、直链）在创建会话时绑定计数回调，交付完成后回调
//     落库，维持现有的下载次数与流量统计口径；
//   - TTL 内允许重复访问（支持 Range 与下载工具重试），到期后地址失效。
const deliveryTTL = 5 * time.Minute

type deliverySession struct {
	BlobID      string
	Name        string
	ContentType string
	SizeBytes   int64
	// SHA256 是内容的明文哈希，随 X-XPH-Content-SHA256 响应头下发，供接收端
	// 下载完成后自行校验（服务端不做交付前校验）。
	SHA256 string
	// ClientDecrypt 为真时输出密文，并在响应头下发解密元数据（浏览器端解密）。
	ClientDecrypt bool
	// EncryptionMeta 是浏览器端解密所需的元数据（含用临时公钥包裹的 DEK）。
	EncryptionMeta map[string]any
	// Purpose 是该次交付的用途：123 云盘据此选择消耗直链流量还是自用下载流量
	// （两条通道都经本机中转，交付的字节完全相同）。
	Purpose blobstore.PresignPurpose
	// OnStart 在读取对象前执行；返回 false 表示业务拒绝（如超过下载限制）。
	OnStart func(ctx context.Context) (bool, error)
	// OnComplete 在交付结束后回调（字节区间用于流量统计）。
	OnComplete func(ctx context.Context, start, end int64, complete bool)
	// ExpiresAt 是会话失效时刻（sweep 据此清理，不区分是否被访问过）。
	ExpiresAt time.Time
}

type deliveryManager struct {
	mu       sync.Mutex
	sessions map[string]*deliverySession
}

func newDeliveryManager() *deliveryManager {
	return &deliveryManager{sessions: map[string]*deliverySession{}}
}

func (m *deliveryManager) create(session *deliverySession) (string, error) {
	id, err := randomtoken.New(24)
	if err != nil {
		return "", err
	}
	session.ExpiresAt = time.Now().Add(deliveryTTL)
	m.mu.Lock()
	m.sessions[id] = session
	m.mu.Unlock()
	return id, nil
}

func (m *deliveryManager) lookup(id string) (*deliverySession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	if time.Now().After(session.ExpiresAt) {
		delete(m.sessions, id)
		return nil, false
	}
	return session, true
}

// sweep 清理过期会话（由交付处理器在每次请求时顺带触发）。
func (m *deliveryManager) sweep() {
	now := time.Now()
	m.mu.Lock()
	for id, session := range m.sessions {
		if now.After(session.ExpiresAt) {
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
}

// deliveryHandler 处理 /dl/<id>：读取会话后交给统一交付实现。
func deliveryHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/dl/")
		if id == "" || strings.Contains(id, "/") {
			writeBusinessError(w, http.StatusNotFound, "下载地址不存在")
			return
		}
		deps.Deliveries.sweep()
		session, ok := deps.Deliveries.lookup(id)
		if !ok {
			writeBusinessError(w, http.StatusGone, "下载地址已失效或已使用")
			return
		}
		serveDeliverySession(w, r, deps, session)
	}
}

// serveDeliverySession 执行一次对象交付：（可选）完整性校验 → 业务预检 →
// 流式输出（支持 Range）→ 计数回调。错误保持 JSON 协议。
// 直链等已有稳定入口的场景直接调用本函数，不经过 /dl 跳转。
func serveDeliverySession(w http.ResponseWriter, r *http.Request, deps Deps, session *deliverySession) {
	ctx := r.Context()
	blob, err := deps.Blobs.Get(ctx, session.BlobID)
	if err != nil {
		log.Printf("交付对象不存在 blob=%s: %v", session.BlobID, err)
		writeBusinessError(w, http.StatusUnprocessableEntity, "文件不存在或已损坏")
		return
	}
	// 服务端不做交付前全量校验（见 blobReader 注释）：完整性由上传时的流式哈希
	// 比对与接收端按 X-XPH-Content-SHA256 自行校验共同保证。
	// HEAD 只是探测（播放器/下载器常见）：不触发额度预扣，避免探测请求占用
	// 下载任务额度（GET 时才真正 Reserve）。
	if session.OnStart != nil && r.Method != http.MethodHead {
		allowed, err := session.OnStart(ctx)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "更新下载状态失败")
			return
		}
		if !allowed {
			writeBusinessError(w, http.StatusTooManyRequests, "分享已过期或达到下载限制")
			return
		}
	}
	delivery, ok := deliverBlobContent(w, r, deps, blob, session.Name, session.ContentType,
		"", session.ClientDecrypt, session.EncryptionMeta, session.Purpose)
	if !ok {
		return
	}
	if session.OnComplete == nil {
		return
	}
	start, end := delivery.start, delivery.end
	if session.ClientDecrypt {
		// 计数换算回明文坐标（与任务登记的明文总量同坐标系）：客户端构造
		// "明文长度的密文 Range" 无法提前触发完整下载判定。
		start, end = plainRangeForWireRange(delivery.start, delivery.end,
			blobstore.EncryptionChunkSize, blob.SizeBytes)
	}
	// 交付完成后的计数使用脱钩上下文：客户端断开不应丢失已传输统计。
	doneCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	session.OnComplete(doneCtx, start, end, delivery.complete)
}

// plainRangeForWireRange 把密文（存储层）字节区间映射回明文坐标。
// 布局：明文按 4MiB 分块加密，每块密文 = 明文 + 16 字节 tag，块间连续；
// 结果按明文总长裁剪，保证与任务登记的 [0, plainSize) 口径一致。
func plainRangeForWireRange(start, end, chunkSize, plainSize int64) (int64, int64) {
	if chunkSize <= 0 {
		return start, end
	}
	wireChunk := chunkSize + blobstore.EncryptionTagOverhead
	toPlain := func(position int64) int64 {
		if position < 0 {
			return 0
		}
		block := position / wireChunk
		offset := position % wireChunk
		if offset > chunkSize {
			offset = chunkSize // 落在 tag 区：视为块尾
		}
		return block*chunkSize + offset
	}
	plainStart := toPlain(start)
	plainEnd := toPlain(end)
	if plainEnd < plainStart {
		plainEnd = plainStart
	}
	if plainSize > 0 {
		if plainEnd > plainSize-1 {
			plainEnd = plainSize - 1
		}
		if plainStart > plainEnd {
			plainStart = plainEnd
		}
	}
	return plainStart, plainEnd
}
