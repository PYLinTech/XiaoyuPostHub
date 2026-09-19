package server

import (
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

	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
)

// 加密交付策略（前端接收定稿）：
//
//	加密对象 + 浏览器请求（带临时公钥）：前端解密优先——下发的密文与解密元数据
//	  由浏览器自行解密、合并；
//	加密对象 + 无公钥请求（HTTP 部署 / 命令行工具）：服务器实时解密兜底输出明文；
//	对外直链 /d/：始终服务器解密合并输出原文（唯一不受浏览器能力影响的入口）。
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

// serveFileContent 输出文件内容供预览使用（分享页预览、文件页预览共用）：
// 加密对象且请求方具备前端解密能力时下发密文 + 密钥信封；否则服务器解密输出
// 明文（无公钥时的兜底）。
func serveFileContent(w http.ResponseWriter, r *http.Request, deps Deps, item resource.Resource, disposition string) {
	blob, err := resourceBlob(r.Context(), deps, item)
	if err != nil {
		writeBusinessError(w, http.StatusUnprocessableEntity, "文件不存在或已损坏")
		return
	}
	if _, ok := deliverItemStream(w, r, deps, blob, item.Name, blobContentType(item), disposition, blobstore.PresignForShare); !ok {
		return
	}
}

// setEncryptionHeader 把解密元数据以 base64(JSON) 写入响应头。
func setEncryptionHeader(w http.ResponseWriter, meta map[string]any) {
	payload, err := json.Marshal(meta)
	if err != nil {
		return
	}
	w.Header().Set(encryptionMetaHeader, base64.StdEncoding.EncodeToString(payload))
}
