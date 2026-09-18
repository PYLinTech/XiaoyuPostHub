// Package loginseal 为账号密码类请求提供传输层加密。
//
// 设计要点：
//
//   - 服务端在启动时于内存中生成 RSA-2048 密钥对，私钥不落盘、不入库、
//     不出现在配置里，进程重启即自动轮换。
//   - 前端先取回公钥与一次性 nonce，再以 RSA 加密账号密码等敏感字段提交；
//     服务端校验后解密并消费 nonce，杜绝密文被截获后重放。
//   - 无论部署在 HTTPS 还是 HTTP，密码字段都以密文离开浏览器。
//   - 填充方式由部署配置固定：HTTPS 部署只接受 RSA-OAEP，纯 HTTP 部署只
//     接受 RSAES-PKCS1-v1_5（浏览器只在安全上下文暴露 Web Crypto）。前端
//     不按浏览器环境自动回退，只使用服务端下发的算法。
//
// 密钥轮换对前端是透明的：公钥与 nonce 通过 /api/user/login/seal 实时下发，
// 进程重启后旧公钥提交会得到 ErrKeyMismatch，前端据此重新取回即可。
package loginseal

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
)

const (
	// AlgorithmOAEP 是 HTTPS 部署使用的填充方式：RSAES-OAEP + SHA-256，
	// 由浏览器原生 Web Crypto 完成加密。
	AlgorithmOAEP = "RSA-OAEP-256"

	// AlgorithmPKCS1v15 是纯 HTTP 部署使用的填充方式。非安全上下文下浏览器
	// 不暴露 Web Crypto，必须依赖纯 JS 实现，而该实现只支持 PKCS#1 v1.5。
	AlgorithmPKCS1v15 = "RSA1_5"

	// keyBits 固定 2048 位：登录载荷只有几百字节，2048 位在强度与加解密
	// 时延之间最平衡，浏览器端生成密文也没有等待感。
	keyBits = 2048
)

var (
	// ErrKeyMismatch 表示请求携带的 keyId 不是当前密钥（通常是服务重启后
	// 前端仍持旧公钥），前端应重新取回公钥后重试。
	ErrKeyMismatch = errors.New("loginseal: 公钥已轮换")
	// ErrNonceInvalid 表示 nonce 不存在、已过期或已被使用。
	ErrNonceInvalid = errors.New("loginseal: nonce 无效或已被使用")
	// ErrAlgorithm 表示请求声明的填充方式与本次部署允许的算法不一致
	// （例如 HTTPS 部署收到 RSA1_5 请求）。
	ErrAlgorithm = errors.New("loginseal: 不支持的加密算法")
	// ErrDecrypt 表示密文无法用私钥解开（篡改、编码错误或使用了别的公钥）。
	ErrDecrypt = errors.New("loginseal: 解密失败")
)

// Seal 持有进程级密钥对、本次部署唯一允许的填充方式与一次性 nonce 池，
// 供 HTTP 层签发公钥与解密载荷。
type Seal struct {
	privateKey   *rsa.PrivateKey
	keyID        string
	publicKeyPEM string
	algorithm    string
	nonces       *nonceStore
}

// New 生成内存密钥对，并按部署形态固定允许的填充方式：
//
//   - httpsEnabled=true：只接受 RSA-OAEP（SHA-256）；
//   - httpsEnabled=false：只接受 RSAES-PKCS1-v1_5。
//
// 密钥不会写入磁盘或数据库，进程退出即消失；另一个填充方式会被直接拒绝，
// 不存在"降级到弱填充"的路径。
func New(httpsEnabled bool) (*Seal, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return nil, fmt.Errorf("loginseal: 生成密钥对失败：%w", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("loginseal: 编码公钥失败：%w", err)
	}
	digest := sha256.Sum256(der)
	algorithm := AlgorithmPKCS1v15
	if httpsEnabled {
		algorithm = AlgorithmOAEP
	}
	return &Seal{
		privateKey: privateKey,
		// keyId 取公钥摘要前 8 字节：既能唯一标识本次密钥，又不泄露密钥材料。
		keyID:        hex.EncodeToString(digest[:8]),
		publicKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
		algorithm:    algorithm,
		nonces:       newNonceStore(),
	}, nil
}

// KeyID 返回当前公钥的短标识，前端提交时需要原样带回。
func (s *Seal) KeyID() string { return s.keyID }

// PublicKeyPEM 返回 SPKI 格式的 PEM 公钥，可直接交给 Web Crypto 或 jsencrypt。
func (s *Seal) PublicKeyPEM() string { return s.publicKeyPEM }

// Algorithm 返回本次部署唯一允许的填充方式，前端必须按它选择加密实现。
func (s *Seal) Algorithm() string { return s.algorithm }

// IssueNonce 签发一个一次性 nonce，供一次提交使用。
func (s *Seal) IssueNonce() (string, error) { return s.nonces.issue() }

// Open 校验并解密一次密封载荷。
//
// 校验顺序：keyId 匹配 → 填充方式与部署配置一致 → nonce 未被使用（原子
// 消费）→ 解密。任一环节失败都返回对应的哨兵错误，调用方只对外暴露统一的
// "请重试"提示，不区分细节，避免给攻击者提供 oracle。
func (s *Seal) Open(keyID, algorithm, nonce string, ciphertext []byte) ([]byte, error) {
	if keyID != s.keyID {
		return nil, ErrKeyMismatch
	}
	if algorithm != s.algorithm {
		return nil, ErrAlgorithm
	}
	// nonce 在解密前消费：即使解密失败，该 nonce 也不能被再次使用。
	if !s.nonces.consume(nonce) {
		return nil, ErrNonceInvalid
	}
	if algorithm == AlgorithmOAEP {
		plaintext, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, s.privateKey, ciphertext, nil)
		if err != nil {
			return nil, ErrDecrypt
		}
		return plaintext, nil
	}
	plaintext, err := rsa.DecryptPKCS1v15(rand.Reader, s.privateKey, ciphertext)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}
