//go:build e2e_pan123

// 真实 123 云盘平台端到端测试（生产调用，谨慎运行）。
//
// 运行方式：
//
//	XPH_PAN123_CLIENT_ID=xxx XPH_PAN123_CLIENT_SECRET=yyy XPH_PAN123_PARENT_ID=76948842 \
//	  go test -tags e2e_pan123 -run TestPan123RealE2E ./blobstore/ -v -timeout 300s
//
// 覆盖 mock 单测无法验证的真实平台行为：
//   - Go 客户端 chunked multipart 编码的上传兼容性（curl 实测带
//     Content-Length，Go 流式上传为 chunked，平台兼容性必须真机验证）；
//   - upload_complete 的真实 20103（"校验中"）轮询路径；
//   - file/detail 参数名（fileID 大写）回归；
//   - remoteReadSeeker 的真实 Range/Seek 行为；
//   - 直链获取与删除（入回收站）。
package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"testing"
)

func TestPan123RealE2E(t *testing.T) {
	clientID := os.Getenv("XPH_PAN123_CLIENT_ID")
	clientSecret := os.Getenv("XPH_PAN123_CLIENT_SECRET")
	parentID, _ := strconv.ParseInt(os.Getenv("XPH_PAN123_PARENT_ID"), 10, 64)
	if clientID == "" || clientSecret == "" || parentID == 0 {
		t.Skip("缺少 XPH_PAN123_CLIENT_ID / XPH_PAN123_CLIENT_SECRET / XPH_PAN123_PARENT_ID")
	}
	backend := NewPan123Backend(Pan123Config{
		ClientID: clientID, ClientSecret: clientSecret,
		ParentFileID: parentID, DirectLink: true,
	})
	// 强制走分片上传路径：同时覆盖 slice(sliceNo=1) 与 upload_complete 的真实
	// 20103 轮询（单步上传不经过轮询）。
	backend.singleUploadLimit = 1

	payload := bytes.Repeat([]byte("go-e2e!"), 1<<20) // 7MB
	wantSHA := sha256.Sum256(payload)
	name := "go-e2e-" + strconv.Itoa(os.Getpid()) + ".bin"

	ctx := context.Background()
	written, ref, err := backend.Put(ctx, name, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("真实平台 Put 失败（chunked multipart 兼容性？）：%v", err)
	}
	t.Logf("上传成功：%d 字节，fileID=%s（分片路径 + 真实 20103 轮询）", written, ref)
	if written != int64(len(payload)) {
		t.Fatalf("写入字节数不一致：%d", written)
	}

	size, err := backend.Stat(ctx, ref)
	if err != nil || size != int64(len(payload)) {
		t.Fatalf("Stat 失败或大小不符：size=%d err=%v（file/detail 参数名回归）", size, err)
	}
	t.Logf("Stat 通过：%d 字节", size)

	// 读回并校验：从 1000 偏移读，覆盖 Range/Seek；拼回前缀比对全量哈希。
	reader, err := backend.Open(ctx, ref)
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	if _, err := reader.Seek(1000, io.SeekStart); err != nil {
		_ = reader.Close()
		t.Fatalf("Seek 失败：%v", err)
	}
	tail, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(tail) != len(payload)-1000 {
		t.Fatalf("Range 读取长度不符：%d != %d", len(tail), len(payload)-1000)
	}
	rebuilt := append(append([]byte{}, payload[:1000]...), tail...)
	gotSHA := sha256.Sum256(rebuilt)
	if hex.EncodeToString(gotSHA[:]) != hex.EncodeToString(wantSHA[:]) {
		t.Fatal("读回内容与上传内容不一致")
	}
	t.Log("读回校验通过（Seek + Range + 全量 SHA-256 一致）")

	url, ok, err := backend.Presign(ctx, ref, 0, PresignForDirect)
	if err != nil || !ok {
		t.Fatalf("Presign 失败：ok=%v err=%v", ok, err)
	}
	t.Logf("直链：%s", url)

	// 空对象（0 字节）：平台支持，且系统不限制空文件上传——验证 Go 客户端的
	// 空内容单步上传、Stat 0、直接读取 EOF、删除全路径。
	emptyWritten, emptyRef, err := backend.Put(ctx, name+"-empty.bin", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("空对象上传失败：%v", err)
	}
	if emptyWritten != 0 {
		t.Fatalf("空对象写入字节数应为 0，实际 %d", emptyWritten)
	}
	if size, statErr := backend.Stat(ctx, emptyRef); statErr != nil || size != 0 {
		t.Fatalf("空对象 Stat 应为 0：size=%d err=%v", size, statErr)
	}
	emptyReader, err := backend.Open(ctx, emptyRef)
	if err != nil {
		t.Fatalf("空对象 Open 失败：%v", err)
	}
	if payload, readErr := io.ReadAll(emptyReader); readErr != nil || len(payload) != 0 {
		_ = emptyReader.Close()
		t.Fatalf("空对象读取应直接 EOF：len=%d err=%v", len(payload), readErr)
	}
	_ = emptyReader.Close()
	t.Logf("空对象通过：上传 0 字节 → Stat 0 → 读取 EOF（fileID=%s）", emptyRef)

	// 批量删除（主对象 + 空对象 + 一个不存在的 ID）：验证平台的批量语义与
	// 「已删除/不存在均返回成功」的幂等行为。
	if err := backend.DeleteBatch(ctx, []string{ref, emptyRef, "999999999"}); err != nil {
		t.Fatalf("DeleteBatch 失败：%v", err)
	}
	t.Log("DeleteBatch 通过（批量入回收站，重复/不存在 ID 仍成功）")
	t.Log("端到端全链路通过：Put(分片/chunked) → Stat → Seek/Read → Presign → 空对象 → DeleteBatch")
}
