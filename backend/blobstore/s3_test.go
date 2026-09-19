package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// s3Mock 是最小可复现的 S3 兼容服务模拟（path-style，单 bucket）。
type s3Mock struct {
	server *httptest.Server

	mu           sync.Mutex
	objects      map[string][]byte
	requireRange bool
	putCalls     int
	authMissing  int
}

func newS3Mock(t *testing.T) *s3Mock {
	t.Helper()
	mock := &s3Mock{objects: map[string][]byte{}, requireRange: true}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.dispatch))
	t.Cleanup(mock.server.Close)
	return mock
}

func (m *s3Mock) dispatch(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		m.authMissing++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	bucketIndex := strings.Index(r.URL.Path, "/test-bucket/")
	if bucketIndex < 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	key := r.URL.Path[bucketIndex+len("/test-bucket/"):]
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		declared := r.Header.Get("x-amz-content-sha256")
		actual := sha256.Sum256(body)
		if declared != hex.EncodeToString(actual[:]) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		m.putCalls++
		m.objects[key] = body
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		content, ok := m.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if rangeHeader := r.Header.Get("Range"); rangeHeader != "" && m.requireRange {
			var start int64
			if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-", &start); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if start >= int64(len(content)) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[start:])
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	case http.MethodHead:
		content, ok := m.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		delete(m.objects, key) // S3 语义：删除不存在对象也返回 204
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newS3TestBackend(mock *s3Mock) *S3Backend {
	return NewS3Backend(S3Config{
		Endpoint:  mock.server.URL,
		Region:    "us-east-1",
		Bucket:    "test-bucket",
		PathStyle: true,
		Credentials: S3Credentials{
			AccessKeyID:     "test-ak",
			SecretAccessKey: "test-sk",
		},
	})
}

// TestS3PutStatAndDelete 覆盖写入、查询与删除（幂等）。
func TestS3PutStatAndDelete(t *testing.T) {
	mock := newS3Mock(t)
	backend := newS3TestBackend(mock)
	payload := bytes.Repeat([]byte("s3-payload-"), 512)

	written, storedRef, err := backend.Put(context.Background(), "blob-1", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Put 失败：%v", err)
	}
	if written != int64(len(payload)) || storedRef != "blob-1" {
		t.Fatalf("写入结果异常：written=%d ref=%q", written, storedRef)
	}
	if !bytes.Equal(mock.objects["blob-1"], payload) {
		t.Fatalf("远端内容与上传内容不一致")
	}
	size, err := backend.Stat(context.Background(), "blob-1")
	if err != nil || size != int64(len(payload)) {
		t.Fatalf("Stat 异常：size=%d err=%v", size, err)
	}
	if err := backend.Delete(context.Background(), "blob-1"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	if _, err := backend.Stat(context.Background(), "blob-1"); err == nil {
		t.Fatalf("删除后 Stat 应返回对象不存在")
	}
	// 删除不存在的对象应为幂等成功。
	if err := backend.Delete(context.Background(), "blob-1"); err != nil {
		t.Fatalf("重复删除应幂等成功：%v", err)
	}
}

// TestS3OpenRange 覆盖 Range 读取（206）与偏移正确性。
func TestS3OpenRange(t *testing.T) {
	mock := newS3Mock(t)
	backend := newS3TestBackend(mock)
	payload := bytes.Repeat([]byte("0123456789"), 200)
	mock.objects["blob-range"] = payload

	reader, err := backend.Open(context.Background(), "blob-range")
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer reader.Close()
	if _, err := reader.Seek(1234, io.SeekStart); err != nil {
		t.Fatalf("Seek 失败：%v", err)
	}
	got := make([]byte, 16)
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatalf("Read 失败：%v", err)
	}
	if !bytes.Equal(got, payload[1234:1250]) {
		t.Fatalf("Range 读取内容不一致：%q", string(got))
	}
}

// TestS3OpenWithoutRangeSupport 覆盖远端忽略 Range 时的前缀丢弃。
func TestS3OpenWithoutRangeSupport(t *testing.T) {
	mock := newS3Mock(t)
	mock.requireRange = false
	backend := newS3TestBackend(mock)
	payload := bytes.Repeat([]byte("abcdefghij"), 100)
	mock.objects["blob-norange"] = payload

	reader, err := backend.Open(context.Background(), "blob-norange")
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer reader.Close()
	if _, err := reader.Seek(55, io.SeekStart); err != nil {
		t.Fatalf("Seek 失败：%v", err)
	}
	got := make([]byte, 10)
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatalf("Read 失败：%v", err)
	}
	if string(got) != string(payload[55:65]) {
		t.Fatalf("忽略 Range 时应流式丢弃前缀，实际 %q", string(got))
	}
}

// TestS3Presign 覆盖预签名直链生成。
func TestS3Presign(t *testing.T) {
	mock := newS3Mock(t)
	backend := newS3TestBackend(mock)
	url, ok, err := backend.Presign(context.Background(), "blob-direct", 30*time.Minute, PresignForShare)
	if err != nil || !ok {
		t.Fatalf("Presign 失败：ok=%v err=%v", ok, err)
	}
	for _, expected := range []string{
		"X-Amz-Algorithm=AWS4-HMAC-SHA256",
		"X-Amz-Expires=1800",
		"X-Amz-Signature=",
		"/test-bucket/blob-direct",
	} {
		if !strings.Contains(url, expected) {
			t.Fatalf("预签名 URL 缺少 %s：%s", expected, url)
		}
	}
}

// TestS3PrefixAndBucketPath 覆盖对象前缀与 path-style 路径拼接。
func TestS3PrefixAndBucketPath(t *testing.T) {
	mock := newS3Mock(t)
	backend := NewS3Backend(S3Config{
		Endpoint:  mock.server.URL + "/base",
		Region:    "us-east-1",
		Bucket:    "test-bucket",
		Prefix:    "xph/blobs",
		PathStyle: true,
		Credentials: S3Credentials{
			AccessKeyID:     "test-ak",
			SecretAccessKey: "test-sk",
		},
	})
	if _, _, err := backend.Put(context.Background(), "blob-prefixed", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("Put 失败：%v", err)
	}
	if _, ok := mock.objects["xph/blobs/blob-prefixed"]; !ok {
		keys := make([]string, 0, len(mock.objects))
		for key := range mock.objects {
			keys = append(keys, key)
		}
		t.Fatalf("前缀未生效，实际对象键：%v", keys)
	}
}

// TestS3Configured 覆盖配置校验。
func TestS3Configured(t *testing.T) {
	cases := []struct {
		name string
		cfg  S3Config
		ok   bool
	}{
		{"完整配置", S3Config{
			Endpoint: "https://minio.example.com", Bucket: "b",
			Credentials: S3Credentials{AccessKeyID: "a", SecretAccessKey: "s"},
		}, true},
		{"缺 bucket", S3Config{
			Endpoint:    "https://minio.example.com",
			Credentials: S3Credentials{AccessKeyID: "a", SecretAccessKey: "s"},
		}, false},
		{"缺凭据", S3Config{Endpoint: "https://minio.example.com", Bucket: "b"}, false},
		{"非法地址", S3Config{Endpoint: "minio.example.com", Bucket: "b"}, false},
	}
	for _, testCase := range cases {
		backend := NewS3Backend(testCase.cfg)
		err := backend.Configured()
		if (err == nil) != testCase.ok {
			t.Fatalf("%s：Configured 结果与预期不符（err=%v）", testCase.name, err)
		}
	}
}
