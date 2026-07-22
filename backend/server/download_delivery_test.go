package server

import (
	"net/http/httptest"
	"os"
	"testing"
)

func TestServeDownloadTracksCompletedRanges(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "download-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		header     string
		start, end int64
	}{
		{"", 0, 1023}, {"bytes=0-99", 0, 99}, {"bytes=100-1023", 100, 1023},
	} {
		req := httptest.NewRequest("GET", "/", nil)
		if test.header != "" {
			req.Header.Set("Range", test.header)
		}
		response := httptest.NewRecorder()
		got := serveDownload(response, req, file.Name(), 1024, "file.bin", "application/octet-stream")
		if !got.complete || got.start != test.start || got.end != test.end {
			t.Fatalf("range %q: got %+v status=%d bytes=%d", test.header, got, response.Code, response.Body.Len())
		}
	}
}

func TestServeDownloadDoesNotTrackMultipartRange(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "download-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Range", "bytes=0-9,20-29")
	got := serveDownload(httptest.NewRecorder(), req, file.Name(), 1024, "file.bin", "application/octet-stream")
	if got.complete {
		t.Fatal("multipart range must not be treated as a complete tracked interval")
	}
}
