package server

// 交付计划：前端接收定稿后的统一取数模型。
//
// 一次下载操作（分享页 / 文件页 / 管理端审核）在服务端生成一个「下载计划」：
//   - 计划内每个文件项都带分片元数据（明文口径），前端一律按分片逐片处理，
//     不存在「单分片特殊路径」；
//   - 取数方式二选一：
//       redirect：浏览器逐片直连第三方（每片按需取址），前端逐片解密/拼接；
//       proxy   ：浏览器从本机中转拉取——加密对象下发密文 + 密钥信封（前端解密），
//                 请求方无法前端解密（无公钥，如 HTTP 部署或命令行）时由服务器
//                 实时解密兜底输出明文；
//   - 解密、合并、打包全部在前端完成，服务端只负责鉴权、发地址与计数。

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
	sqlcgen "github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
)

const (
	deliverySourceProxy    = "proxy"
	deliverySourceRedirect = "redirect"
	// redirectTTL 是单片直链的有效期：前端按需取址 + 串行下载，单片耗时远小于它。
	redirectTTL = 10 * time.Minute
)

// errDeliveryUnavailable 表示按当前配置无法交付（302 不可用且降级关闭等）。
// 对外只提示「下载失败」，不暴露内部实现细节。
var errDeliveryUnavailable = errors.New("交付不可用")

// downloadJobTTL 按明文总量给下载任务定有效期（大文件给更长完成时间）。
func downloadJobTTL(totalBytes int64) time.Duration {
	switch {
	case totalBytes <= 128<<20:
		return 10 * time.Minute
	case totalBytes <= 2<<30:
		return time.Hour
	case totalBytes <= 10<<30:
		return 6 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// deliveryItem 是下载计划中的一个文件项（kind=folder 的项没有分片，仅供前端
// 还原空目录结构）。
type deliveryItem struct {
	Kind         string         `json:"kind"`
	ResourceID   string         `json:"resourceId"`
	Name         string         `json:"name"`
	RelativePath string         `json:"relativePath,omitempty"`
	SizeBytes    int64          `json:"sizeBytes"`
	SHA256       string         `json:"sha256,omitempty"`
	MimeType     string         `json:"mimeType,omitempty"`
	Parts        []deliveryPart `json:"parts,omitempty"`
	Encryption   map[string]any `json:"encryption,omitempty"`
	// ContentForm 声明该文件项的取数形态（plaintext | ciphertext），是前后端固定
	// 的契约字段：302 取数时第三方响应头不受我们控制，前端只能按它决定逐片解密
	// 还是直接拼接；本机中转时以响应头 X-XPH-Content-Form 为准，两者不一致前端
	// 会明确报错而不是静默产出坏文件。
	ContentForm string `json:"contentForm,omitempty"`
	// StreamURL 仅本机中转取数：密文流（带信封）或明文流（兜底）。
	StreamURL string `json:"streamUrl,omitempty"`
	// PartURL 仅 302 取数：逐片取址模板，前端拼 <partUrl><index>。
	PartURL string `json:"partUrl,omitempty"`
}

// deliveryPart 是面向前端的分片描述（明文口径）。
type deliveryPart struct {
	Index     int32 `json:"index"`
	PlainSize int64 `json:"plainSize"`
}

// clientCanDecrypt 判断请求方是否具备前端解密能力（是否携带临时公钥）。
func clientCanDecrypt(r *http.Request) bool {
	_, err := clientPublicKey(r)
	return err == nil
}

// decideDeliverySource 是取数方式的纯判定（便于单测）：
// 302 优先且可用 → redirect；302 优先但不可用 → 降级开则 proxy，否则不可交付；
// 中转优先 → 直接 proxy。
func decideDeliverySource(mode string, redirectReady, fallback bool) (string, error) {
	if mode == systemsetting.RetrievalRedirect {
		if redirectReady {
			return deliverySourceRedirect, nil
		}
		if fallback {
			return deliverySourceProxy, nil
		}
		return "", errDeliveryUnavailable
	}
	return deliverySourceProxy, nil
}

// resolveDeliverySource 依据系统取数方式、降级开关与对象能力决定本次交付的取数方式：
//
//	302 优先（默认）：所有文件都具备直链能力、且加密文件可由前端解密 → redirect；
//	                  任一条件不满足时：降级开关开启 → proxy，否则不可交付；
//	中转优先：直接 proxy。
//
// forceRedirect 为真（多文件/文件夹）时固定按 302 优先判定、不受「中转优先」
// 影响：逐文件逐片经服务器中转会显著放大流量与解密开销，只有 302 不可用且
// 开启自动降级时才回落到本机中转。
func resolveDeliverySource(r *http.Request, deps Deps, settings sqlcgen.SystemSetting, knobs systemsetting.Knobs, blobs []blobstore.Blob, forceRedirect bool) (string, error) {
	mode := settings.ShareRetrievalMode
	if forceRedirect {
		mode = systemsetting.RetrievalRedirect
	}
	redirectReady := mode == systemsetting.RetrievalRedirect
	for _, blob := range blobs {
		if !redirectReady {
			break
		}
		if !deps.Blobs.PresignReady(blob) {
			redirectReady = false
			break
		}
		if blob.Encryption != nil && !clientCanDecrypt(r) {
			// 加密对象的 302 必须由前端解密，无公钥时不可用。
			redirectReady = false
			break
		}
	}
	return decideDeliverySource(mode, redirectReady, knobs.RedirectFallback)
}

// buildDeliveryItem 组装一个文件项的交付描述。streamURL/partURL 是调用方按入口
// 拼好的完整取数地址（本机中转流 / 逐片取址模板），空字符串表示该方式不适用。
func buildDeliveryItem(r *http.Request, deps Deps, item resource.Resource, blob blobstore.Blob, streamURL, partURL string) (deliveryItem, error) {
	parts, err := deps.Blobs.Parts(r.Context(), blob)
	if err != nil {
		return deliveryItem{}, err
	}
	planParts := make([]deliveryPart, 0, len(parts))
	for _, part := range parts {
		planParts = append(planParts, deliveryPart{Index: part.Index, PlainSize: part.SizeBytes})
	}
	var meta map[string]any
	if blob.Encryption != nil && clientCanDecrypt(r) {
		prepared, metaErr := clientEncryptionMetadata(r, deps, blob)
		if metaErr != nil {
			return deliveryItem{}, metaErr
		}
		meta = prepared
	}
	itemPlan := deliveryItem{
		Kind: resource.KindFile, ResourceID: item.ID, Name: item.Name,
		SizeBytes: blob.SizeBytes, SHA256: blob.SHA256,
		Parts: planParts, Encryption: meta, StreamURL: streamURL, PartURL: partURL,
		ContentForm: deliveryContentForm(r, blob, streamURL != ""),
	}
	if item.MimeType != nil {
		itemPlan.MimeType = strings.TrimSpace(*item.MimeType)
	}
	return itemPlan, nil
}

// deliveryContentForm 声明该文件项在本次取数中实际传输的形态，是前后端固定的
// 契约字段（与响应头 X-XPH-Content-Form 同源规则）：
//
//	302 取数（proxy=false）：取到的是存储层原样字节——加密对象即密文；
//	本机中转（proxy=true）：请求方带临时公钥才下发密文，否则服务器解密兜底输出明文。
//
// 判定必须与 deliverBlobContent 的实际行为一致，否则前端会按错误的形态处理字节。
func deliveryContentForm(r *http.Request, blob blobstore.Blob, proxy bool) string {
	if blob.Encryption != nil && (!proxy || clientCanDecrypt(r)) {
		return contentFormCiphertext
	}
	return contentFormPlaintext
}

// presignItemPart 为某文件的指定分片取第三方直链（按需取址：前端逐片请求）。
func presignItemPart(r *http.Request, deps Deps, blob blobstore.Blob, index int32, purpose blobstore.PresignPurpose) (string, error) {
	parts, err := deps.Blobs.Parts(r.Context(), blob)
	if err != nil {
		return "", err
	}
	for _, part := range parts {
		if part.Index != index {
			continue
		}
		url, ok, presignErr := deps.Blobs.PresignPart(r.Context(), blob, part, redirectTTL, purpose)
		if presignErr != nil {
			return "", presignErr
		}
		if !ok || strings.TrimSpace(url) == "" {
			return "", errDeliveryUnavailable
		}
		return url, nil
	}
	return "", errDeliveryUnavailable
}

// writeDeliveryFailure 统一交付失败提示：不向用户暴露 302/直链/降级等内部逻辑。
func writeDeliveryFailure(w http.ResponseWriter) {
	writeBusinessError(w, http.StatusServiceUnavailable, "下载失败，请稍后重试")
}

// deliverItemStream 输出单个文件项（本机中转取数）：
//   - 加密对象且请求方具备前端解密能力 → 密文流 + 密钥信封；
//   - 否则（明文对象 / 请求方无法前端解密）→ 服务器解密后输出明文。
func deliverItemStream(w http.ResponseWriter, r *http.Request, deps Deps, blob blobstore.Blob,
	name, contentType, disposition string, purpose blobstore.PresignPurpose) (downloadDelivery, bool) {
	if blob.Encryption != nil && clientCanDecrypt(r) {
		meta, err := clientEncryptionMetadata(r, deps, blob)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "准备下载失败")
			return downloadDelivery{}, false
		}
		return deliverBlobContent(w, r, deps, blob, name, contentType, disposition, true, meta, purpose)
	}
	return deliverBlobContent(w, r, deps, blob, name, contentType, disposition, false, nil, purpose)
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
