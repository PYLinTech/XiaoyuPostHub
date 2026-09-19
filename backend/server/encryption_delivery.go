package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
)

// 加密交付策略（与冻结方案一致）：
//
//	明文对象                       → 直接交付；
//	加密对象 + 服务器实时解密开启   → 服务器解密后输出明文；
//	加密对象 + 服务器实时解密关闭   → 输出密文，解密元数据（含用浏览器临时
//	                                公钥包裹的 DEK）随响应下发，浏览器端解密；
//	直链                           → 始终服务器解密（forceServerDecrypt=true）。
//
// 密钥非对称下发：浏览器每次请求现场生成临时 RSA-OAEP 密钥对，公钥通过
// X-XPH-Client-Public-Key 请求头（base64(SPKI DER)）传入；服务端只下发用该
// 公钥加密的 DEK 信封，私钥始终不出浏览器内存。
const (
	clientPublicKeyHeader = "X-XPH-Client-Public-Key"
	// encryptionMetaHeader 携带 base64(JSON) 形式的解密元数据（HTTP 头禁止非
	// ASCII，因此整体 base64 编码）。
	encryptionMetaHeader = "X-XPH-Encryption"
)

var errClientKeyMissing = errors.New("缺少客户端公钥")

// clientPublicKey 解析前端临时公钥：base64(SPKI DER) → RSA 公钥（≥2048 位）。
func clientPublicKey(r *http.Request) (*rsa.PublicKey, error) {
	raw := strings.TrimSpace(r.Header.Get(clientPublicKeyHeader))
	if raw == "" {
		return nil, errClientKeyMissing
	}
	der, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("客户端公钥编码无效")
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("客户端公钥格式无效")
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok || pub.N.BitLen() < 2048 {
		return nil, fmt.Errorf("客户端公钥强度不足")
	}
	return pub, nil
}

// clientEncryptionMetadata 为"浏览器端解密"的交付构造解密元数据：
// 明文参数 + 用客户端公钥包裹的 DEK。blob 未加密时返回 nil。
func clientEncryptionMetadata(r *http.Request, deps Deps, blob blobstore.Blob) (map[string]any, error) {
	if blob.Encryption == nil {
		return nil, nil
	}
	dek, err := deps.Blobs.DecryptDEK(blob)
	if err != nil {
		return nil, err
	}
	pub, err := clientPublicKey(r)
	if err != nil {
		return nil, err
	}
	envelope, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, dek, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"algorithm":   blob.Encryption.Algo,
		"chunkSize":   blobstore.EncryptionChunkSize,
		"fileNonce":   base64.StdEncoding.EncodeToString(blob.Encryption.Nonce),
		"keyId":       blob.Encryption.KeyID,
		"keyEnvelope": base64.StdEncoding.EncodeToString(envelope),
		"sizeBytes":   blob.SizeBytes,
		"wireSize":    blob.WireSize(),
	}, nil
}

// needsClientDecryption 判断一次交付是否需要浏览器端解密。
// forceServerDecrypt 用于直链（始终服务器解密，无浏览器参与）。
func needsClientDecryption(blob blobstore.Blob, realtimeDecrypt, forceServerDecrypt bool) bool {
	if blob.Encryption == nil || forceServerDecrypt {
		return false
	}
	return !realtimeDecrypt
}

// presignBlob 尝试为对象生成第三方直链（302 交付）。任何不支持或失败都返回
// ok=false，由调用方降级为本机中转——302 是优化路径，不应导致交付失败。
//
// purpose 决定走哪份额度：分享/文件页与站内直链可以分别配置（123 云盘的直链流量
// 与自用下载流量是两份独立额度）。
func presignBlob(ctx context.Context, deps Deps, blob blobstore.Blob, purpose blobstore.PresignPurpose) (string, bool) {
	url, ok, err := deps.Blobs.Presign(ctx, blob, 10*time.Minute, purpose)
	if err != nil || !ok {
		return "", false
	}
	return url, true
}

// serveFileContent 按当前策略输出文件内容（下载与预览共用）：
//   - 明文对象（或服务器实时解密）：直接流式输出——服务端不做交付前全量校验，
//     完整性由上传时的流式哈希比对与接收端按 X-XPH-Content-SHA256 自行校验保证；
//   - 浏览器端解密：输出密文并在响应头下发解密元数据；缺少客户端公钥时返回
//     428，提示刷新页面后重试。
func serveFileContent(w http.ResponseWriter, r *http.Request, deps Deps, item resource.Resource, disposition string) {
	serveFileContentWithOptions(w, r, deps, item, disposition, false)
}

// serveFileContentWithOptions 是 serveFileContent 的可配置版本。
// allowRedirect 为真时（文件页下载），明文对象在「302 交付」配置下直接重定向到
// 第三方直链；预览固定中转（需要 Range 与解密元数据），加密对象固定中转（密钥
// 信封无法随第三方响应下发）。
func serveFileContentWithOptions(w http.ResponseWriter, r *http.Request, deps Deps, item resource.Resource, disposition string, allowRedirect bool) {
	blob, err := resourceBlob(r.Context(), deps, item)
	if err != nil {
		writeBusinessError(w, http.StatusUnprocessableEntity, "文件不存在或已损坏")
		return
	}
	settings, err := deps.SystemSettings.Get(r.Context())
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取交付策略失败")
		return
	}
	if allowRedirect && blob.Encryption == nil && settings.ShareRetrievalMode == systemsetting.RetrievalRedirect {
		// 先用廉价的 Stat 确认对象仍然存在，避免把用户跳到第三方的 404。
		if availableErr := deps.Blobs.VerifyAvailable(r.Context(), blob); availableErr == nil {
			// 文件页下载归入「分享」这一档额度偏好。
			if redirectURL, ok := presignBlob(r.Context(), deps, blob, blobstore.PresignForShare); ok {
				http.Redirect(w, r, redirectURL, http.StatusFound)
				return
			}
		}
	}
	if needsClientDecryption(blob, settings.ProxyRealtimeDecrypt, false) {
		meta, metaErr := clientEncryptionMetadata(r, deps, blob)
		if metaErr != nil {
			if errors.Is(metaErr, errClientKeyMissing) {
				writeBusinessError(w, http.StatusPreconditionRequired, "该文件需要在浏览器端解密，请刷新页面后重试")
				return
			}
			writeBusinessError(w, http.StatusInternalServerError, "准备解密信息失败")
			return
		}
		if _, ok := deliverBlobContent(w, r, deps, blob, item.Name, blobContentType(item), disposition, true, meta, blobstore.PresignForShare); !ok {
			return
		}
		return
	}
	// 明文对象 / 服务器实时解密：直接用已读到的 blob 元数据输出，不再二次查询。
	if _, ok := deliverBlobContent(w, r, deps, blob, item.Name, blobContentType(item), disposition, false, nil, blobstore.PresignForShare); !ok {
		return
	}
}

func setEncryptionHeader(w http.ResponseWriter, meta map[string]any) {
	payload, err := json.Marshal(meta)
	if err != nil {
		return
	}
	w.Header().Set(encryptionMetaHeader, base64.StdEncoding.EncodeToString(payload))
}
