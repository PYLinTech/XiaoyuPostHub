package loginseal

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"
)

// publicKeyFor 从 Seal 下发的 PEM 解析公钥，顺带验证 PEM 可被标准库与
// 前端（Web Crypto / jsencrypt）正常消费的格式约定。
func publicKeyFor(t *testing.T, seal *Seal) *rsa.PublicKey {
	t.Helper()
	block, _ := pem.Decode([]byte(seal.PublicKeyPEM()))
	if block == nil {
		t.Fatal("PublicKeyPEM 不是有效的 PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("解析公钥失败：%v", err)
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("公钥类型 = %T，期望 *rsa.PublicKey", parsed)
	}
	return publicKey
}

func encryptOAEP(t *testing.T, seal *Seal, plaintext []byte) []byte {
	t.Helper()
	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKeyFor(t, seal), plaintext, nil)
	if err != nil {
		t.Fatalf("OAEP 加密失败：%v", err)
	}
	return ciphertext
}

func encryptPKCS1v15(t *testing.T, seal *Seal, plaintext []byte) []byte {
	t.Helper()
	ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, publicKeyFor(t, seal), plaintext)
	if err != nil {
		t.Fatalf("PKCS#1 v1.5 加密失败：%v", err)
	}
	return ciphertext
}

func newSeal(t *testing.T, httpsEnabled bool) *Seal {
	t.Helper()
	seal, err := New(httpsEnabled)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	return seal
}

func issueNonce(t *testing.T, seal *Seal) string {
	t.Helper()
	nonce, err := seal.IssueNonce()
	if err != nil {
		t.Fatalf("IssueNonce 失败：%v", err)
	}
	if nonce == "" {
		t.Fatal("IssueNonce 返回空 nonce")
	}
	return nonce
}

func TestAlgorithmFollowsDeployment(t *testing.T) {
	if got := newSeal(t, true).Algorithm(); got != AlgorithmOAEP {
		t.Fatalf("HTTPS 部署算法 = %q，期望 %q", got, AlgorithmOAEP)
	}
	if got := newSeal(t, false).Algorithm(); got != AlgorithmPKCS1v15 {
		t.Fatalf("HTTP 部署算法 = %q，期望 %q", got, AlgorithmPKCS1v15)
	}
}

func TestOpenDecryptsOAEP(t *testing.T) {
	seal := newSeal(t, true)
	plaintext := []byte(`{"userName":"admin","password":"Passw0rd!"}`)
	nonce := issueNonce(t, seal)

	got, err := seal.Open(seal.KeyID(), AlgorithmOAEP, nonce, encryptOAEP(t, seal, plaintext))
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("明文 = %q，期望 %q", got, plaintext)
	}
}

func TestOpenDecryptsPKCS1v15(t *testing.T) {
	seal := newSeal(t, false)
	plaintext := []byte(`{"password":"Passw0rd!"}`)
	nonce := issueNonce(t, seal)

	got, err := seal.Open(seal.KeyID(), AlgorithmPKCS1v15, nonce, encryptPKCS1v15(t, seal, plaintext))
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("明文 = %q，期望 %q", got, plaintext)
	}
}

func TestHTTPSDeploymentRejectsLegacyPadding(t *testing.T) {
	seal := newSeal(t, true)
	nonce := issueNonce(t, seal)
	_, err := seal.Open(seal.KeyID(), AlgorithmPKCS1v15, nonce, encryptPKCS1v15(t, seal, []byte("payload")))
	if !errors.Is(err, ErrAlgorithm) {
		t.Fatalf("err = %v，期望 ErrAlgorithm（HTTPS 部署不接受 RSA1_5）", err)
	}
}

func TestHTTPDeploymentRejectsOAEP(t *testing.T) {
	seal := newSeal(t, false)
	nonce := issueNonce(t, seal)
	_, err := seal.Open(seal.KeyID(), AlgorithmOAEP, nonce, encryptOAEP(t, seal, []byte("payload")))
	if !errors.Is(err, ErrAlgorithm) {
		t.Fatalf("err = %v，期望 ErrAlgorithm（HTTP 部署不接受 RSA-OAEP）", err)
	}
}

func TestOpenRejectsReusedNonce(t *testing.T) {
	seal := newSeal(t, true)
	nonce := issueNonce(t, seal)
	ciphertext := encryptOAEP(t, seal, []byte("payload"))

	if _, err := seal.Open(seal.KeyID(), AlgorithmOAEP, nonce, ciphertext); err != nil {
		t.Fatalf("首次 Open 失败：%v", err)
	}
	// 同一密文与 nonce 再放送一次（重放）必须被拒绝。
	if _, err := seal.Open(seal.KeyID(), AlgorithmOAEP, nonce, ciphertext); !errors.Is(err, ErrNonceInvalid) {
		t.Fatalf("重放 err = %v，期望 ErrNonceInvalid", err)
	}
}

func TestOpenRejectsUnknownNonce(t *testing.T) {
	seal := newSeal(t, true)
	ciphertext := encryptOAEP(t, seal, []byte("payload"))
	for _, nonce := range []string{"", "not-a-real-nonce"} {
		if _, err := seal.Open(seal.KeyID(), AlgorithmOAEP, nonce, ciphertext); !errors.Is(err, ErrNonceInvalid) {
			t.Fatalf("nonce=%q err = %v，期望 ErrNonceInvalid", nonce, err)
		}
	}
}

func TestOpenRejectsKeyMismatch(t *testing.T) {
	seal := newSeal(t, true)
	other := newSeal(t, true)
	if seal.KeyID() == other.KeyID() {
		t.Fatal("两次生成的 keyID 不应相同")
	}
	nonce := issueNonce(t, seal)
	if _, err := seal.Open(other.KeyID(), AlgorithmOAEP, nonce, encryptOAEP(t, seal, []byte("payload"))); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("err = %v，期望 ErrKeyMismatch", err)
	}
}

func TestOpenRejectsUnknownAlgorithm(t *testing.T) {
	seal := newSeal(t, true)
	nonce := issueNonce(t, seal)
	if _, err := seal.Open(seal.KeyID(), "RSA-NONE", nonce, []byte("ciphertext")); !errors.Is(err, ErrAlgorithm) {
		t.Fatalf("err = %v，期望 ErrAlgorithm", err)
	}
	// 算法不匹配时不应消费 nonce，避免正常提交被无关请求挤掉。
	if _, err := seal.Open(seal.KeyID(), AlgorithmOAEP, nonce, encryptOAEP(t, seal, []byte("ok"))); err != nil {
		t.Fatalf("nonce 不应因算法错误被消费：%v", err)
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	seal := newSeal(t, true)
	ciphertext := encryptOAEP(t, seal, []byte("payload"))
	ciphertext[0] ^= 0xff
	if _, err := seal.Open(seal.KeyID(), AlgorithmOAEP, issueNonce(t, seal), ciphertext); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("err = %v，期望 ErrDecrypt", err)
	}
}

func TestNonceStoreExpiry(t *testing.T) {
	store := &nonceStore{expires: make(map[string]time.Time), ttl: time.Millisecond, capacity: 8}
	nonce, err := store.issue()
	if err != nil {
		t.Fatalf("issue 失败：%v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if store.consume(nonce) {
		t.Fatal("过期 nonce 不应通过校验")
	}
}

func TestNonceStoreCapacityEviction(t *testing.T) {
	store := &nonceStore{expires: make(map[string]time.Time), ttl: time.Minute, capacity: 2}
	first, err := store.issue()
	if err != nil {
		t.Fatalf("issue 失败：%v", err)
	}
	if _, err := store.issue(); err != nil {
		t.Fatalf("issue 失败：%v", err)
	}
	// 达到容量后再签发一条，最旧的 first 应被淘汰。
	if _, err := store.issue(); err != nil {
		t.Fatalf("issue 失败：%v", err)
	}
	if store.consume(first) {
		t.Fatal("最旧的 nonce 应被容量淘汰")
	}
}
