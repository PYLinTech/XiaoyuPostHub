package server

import (
	"net/http/httptest"
	"os"
	"testing"
)

// TestServeBlobStreamTracksCompletedRanges 覆盖对象流交付的 Range 追踪：
// 完整取流与单段 Range 都算作"覆盖 [start,end]"，多段 Range 一律拒绝。
func TestServeBlobStreamTracksCompletedRanges(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/download.bin"
	if err := os.WriteFile(path, make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		header     string
		start, end int64
	}{
		{"", 0, 1023}, {"bytes=0-99", 0, 99}, {"bytes=100-1023", 100, 1023},
	} {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/", nil)
		if test.header != "" {
			req.Header.Set("Range", test.header)
		}
		response := httptest.NewRecorder()
		got := serveBlobStreamWith(response, req, file, 1024, "file.bin", "application/octet-stream", "attachment")
		if !got.complete || got.start != test.start || got.end != test.end {
			t.Fatalf("range %q: got %+v status=%d bytes=%d", test.header, got, response.Code, response.Body.Len())
		}
	}
}

func TestServeBlobStreamRejectsMultipartRange(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/download.bin"
	if err := os.WriteFile(path, make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Range", "bytes=0-9,20-29")
	response := httptest.NewRecorder()
	got := serveBlobStreamWith(response, req, file, 1024, "file.bin", "application/octet-stream", "attachment")
	if got.complete {
		t.Fatal("multipart range must not be treated as a complete tracked interval")
	}
	if response.Code != 416 {
		t.Fatalf("multipart range status = %d, want 416", response.Code)
	}
}
