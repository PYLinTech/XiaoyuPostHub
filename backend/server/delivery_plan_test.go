package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
)

// TestDownloadJobTTL 覆盖下载任务有效期的分档规则（大文件给更长时间）。
func TestDownloadJobTTL(t *testing.T) {
	cases := []struct {
		total int64
		want  time.Duration
	}{
		{1, 10 * time.Minute},
		{128 << 20, 10 * time.Minute},
		{128<<20 + 1, time.Hour},
		{2 << 30, time.Hour},
		{2<<30 + 1, 6 * time.Hour},
		{10 << 30, 6 * time.Hour},
		{10<<30 + 1, 24 * time.Hour},
	}
	for _, test := range cases {
		if got := downloadJobTTL(test.total); got != test.want {
			t.Fatalf("downloadJobTTL(%d) = %v, want %v", test.total, got, test.want)
		}
	}
}

// TestDecideDeliverySource 覆盖取数方式判定：
//   - 302 优先且可用 → redirect；
//   - 302 优先但不可用 → 降级开=本机中转，降级关=交付失败；
//   - 中转优先 → 本机中转。
func TestDecideDeliverySource(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		ready      bool
		fallback   bool
		wantSource string
		wantErr    bool
	}{
		{"302优先可用", systemsetting.RetrievalRedirect, true, false, deliverySourceRedirect, false},
		{"302优先不可用且自动降级", systemsetting.RetrievalRedirect, false, true, deliverySourceProxy, false},
		{"302优先不可用且不降级", systemsetting.RetrievalRedirect, false, false, "", true},
		{"中转优先", systemsetting.RetrievalProxy, true, false, deliverySourceProxy, false},
		{"中转优先忽略直链可用性", systemsetting.RetrievalProxy, false, false, deliverySourceProxy, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			source, err := decideDeliverySource(test.mode, test.ready, test.fallback)
			if test.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, test.wantErr)
			}
			if source != test.wantSource {
				t.Fatalf("source = %q, want %q", source, test.wantSource)
			}
		})
	}
}

// TestClientCanDecrypt 覆盖"前端解密能力"判定：仅携带合法临时公钥时可用。
func TestClientCanDecrypt(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	if clientCanDecrypt(req) {
		t.Fatal("无公钥的请求不应视为可前端解密")
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(clientPublicKeyHeader, base64.StdEncoding.EncodeToString(der))
	if !clientCanDecrypt(req) {
		t.Fatal("合法临时公钥应视为可前端解密")
	}

	req.Header.Set(clientPublicKeyHeader, "not-base64!!")
	if clientCanDecrypt(req) {
		t.Fatal("非法公钥不应视为可前端解密")
	}
}

// TestPlainRangeForWireRange 覆盖密文坐标 → 明文坐标的换算与裁剪。
func TestPlainRangeForWireRange(t *testing.T) {
	chunk := blobstore.EncryptionChunkSize

	// 单块对象：完整密文区间映射回明文后被裁到实际长度。
	plainSize := int64(100)
	wireSize := plainSize + blobstore.EncryptionTagOverhead
	start, end := plainRangeForWireRange(0, wireSize-1, chunk, plainSize)
	if start != 0 || end != plainSize-1 {
		t.Fatalf("单块换算 = (%d,%d), want (0,%d)", start, end, plainSize-1)
	}

	// 第二块起点：密文偏移为 chunk+tag，对应明文偏移 chunk。
	start, end = plainRangeForWireRange(chunk+blobstore.EncryptionTagOverhead, chunk+blobstore.EncryptionTagOverhead, chunk, 10*chunk)
	if start != chunk || end != chunk {
		t.Fatalf("块首换算 = (%d,%d), want (%d,%d)", start, end, chunk, chunk)
	}

	// 末块未满：end 被裁剪到明文末字节。
	plainSize = chunk + 10
	wireSize = plainSize + 2*blobstore.EncryptionTagOverhead
	start, end = plainRangeForWireRange(chunk+blobstore.EncryptionTagOverhead, wireSize-1, chunk, plainSize)
	if start != chunk || end != plainSize-1 {
		t.Fatalf("末块换算 = (%d,%d), want (%d,%d)", start, end, chunk, plainSize-1)
	}
}
