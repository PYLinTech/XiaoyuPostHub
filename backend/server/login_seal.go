package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/PYLinTech/XiaoyuPostHub/backend/loginseal"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

// sealedRequest 是携带密码的请求统一使用的加密信封。
//
// 明文密码不再出现在任何请求体里：Payload 是 RSA 密文的 base64，
// 解密后的 JSON 结构由各接口自行定义（见 loginPayload / registerPayload 等）。
type sealedRequest struct {
	KeyID     string `json:"keyId"`
	Algorithm string `json:"algorithm"`
	Nonce     string `json:"nonce"`
	Payload   string `json:"payload"`
}

// sealStaleCode 是"公钥已轮换 / nonce 失效"的机器可读标识。前端据此重新
// 取回公钥并重试，用户无需理解发生了什么。
const sealStaleCode = "seal_stale"

// loginSealHandler 下发本次进程的公钥、唯一允许的加密算法与一次性 nonce。
//
// 每次提交都应重新调用：nonce 一次性、5 分钟有效，公钥随进程重启轮换。
// 因此响应禁止任何缓存。
func loginSealHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if deps.PasswordSeal == nil {
			writeBusinessError(w, http.StatusInternalServerError, "安全通道不可用")
			return
		}
		nonce, err := deps.PasswordSeal.IssueNonce()
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "安全通道不可用")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{
			"status":    "ok",
			"keyId":     deps.PasswordSeal.KeyID(),
			"publicKey": deps.PasswordSeal.PublicKeyPEM(),
			"nonce":     nonce,
			// 算法由部署配置固定，前端必须按它选择加密实现，不做环境自动回退。
			"algorithm": deps.PasswordSeal.Algorithm(),
		})
	}
}

// openSealedPayload 解析加密信封、解密并把明文 JSON 反序列化到 dst。
//
// 返回 false 时已经写好了响应，调用方直接 return 即可。对外只区分两类失败：
//   - 信封本身不合法 → 400「请求格式错误」
//   - 公钥轮换 / nonce 失效 / 解密失败 → 400 + seal_stale 码，前端自动重试
//
// 不向客户端区分"解密失败"与"nonce 失效"的细节，避免给攻击者提供 oracle。
func openSealedPayload(w http.ResponseWriter, r *http.Request, deps Deps, dst any) bool {
	if deps.PasswordSeal == nil {
		writeBusinessError(w, http.StatusInternalServerError, "安全通道不可用")
		return false
	}
	var envelope sealedRequest
	if err := decodeJSON(w, r, &envelope); err != nil {
		writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
		return false
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil || len(ciphertext) == 0 {
		writeSealStale(w)
		return false
	}
	plaintext, err := deps.PasswordSeal.Open(envelope.KeyID, envelope.Algorithm, envelope.Nonce, ciphertext)
	if err != nil {
		if errors.Is(err, loginseal.ErrKeyMismatch) || errors.Is(err, loginseal.ErrNonceInvalid) || errors.Is(err, loginseal.ErrDecrypt) {
			writeSealStale(w)
			return false
		}
		writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
		return false
	}
	return true
}

func writeSealStale(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"status": "error",
		"msg":    "安全校验已过期，请重试",
		"code":   sealStaleCode,
	})
}

// passwordPolicyMessage 把账号与密码规则（user 包）的校验错误翻译成
// 前端可直接展示的提示。注册与管理员重设密码共用。
func passwordPolicyMessage(err error) string {
	switch {
	case errors.Is(err, user.ErrUsernameLength):
		return "账号需为 3 至 18 位"
	case errors.Is(err, user.ErrUsernameCharset):
		return "账号不能包含空白或控制字符"
	case errors.Is(err, user.ErrPasswordLength):
		return "密码需为 8 至 18 位"
	case errors.Is(err, user.ErrPasswordTooWeak):
		return "密码太简单，请混合使用字母、数字或符号"
	default:
		return "账号或密码不符合要求"
	}
}
