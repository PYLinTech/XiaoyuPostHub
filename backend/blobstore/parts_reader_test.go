package blobstore

import (
	"bytes"
	"context"
	"io"
	"testing"
)

// memBackend 是 partsReader 测试用的内存后端。
type memBackend struct{ data map[string][]byte }

func newMemBackend() *memBackend { return &memBackend{data: map[string][]byte{}} }

func (m *memBackend) Kind() string { return "mem" }

func (m *memBackend) Put(_ context.Context, ref string, r io.Reader) (int64, string, error) {
	payload, err := io.ReadAll(r)
	if err != nil {
		return 0, "", err
	}
	m.data[ref] = payload
	return int64(len(payload)), ref, nil
}

func (m *memBackend) Open(_ context.Context, ref string) (io.ReadSeekCloser, error) {
	payload, ok := m.data[ref]
	if !ok {
		return nil, ErrBlobNotFound
	}
	return &memReadSeeker{Reader: bytes.NewReader(payload)}, nil
}

func (m *memBackend) Delete(_ context.Context, ref string) error {
	delete(m.data, ref)
	return nil
}

func (m *memBackend) Stat(_ context.Context, ref string) (int64, error) {
	payload, ok := m.data[ref]
	if !ok {
		return 0, ErrBlobNotFound
	}
	return int64(len(payload)), nil
}

func (m *memBackend) Copy(_ context.Context, srcRef, dstRef string) error {
	payload, ok := m.data[srcRef]
	if !ok {
		return ErrBlobNotFound
	}
	m.data[dstRef] = append([]byte(nil), payload...)
	return nil
}

type memReadSeeker struct{ *bytes.Reader }

func (m *memReadSeeker) Close() error { return nil }

// TestPartsReaderRandomRead 验证分片流的随机读取与整体内容一致：片首、片内
// 偏移、同片内跳转、跨片、末尾读取都必须返回正确数据。
//
// 回归背景：早期实现首次进入某片（或同片内 Seek 跳转）时没有把底层流定位
// 到片内偏移，随机读会从错误位置返回数据——全量顺序读恰好正常，掩盖了该
// 缺陷；一旦多物理分片对象走 Range/预览/断点续传就会返回错位内容。
func TestPartsReaderRandomRead(t *testing.T) {
	ctx := context.Background()
	backend := newMemBackend()

	total := make([]byte, 25)
	for index := range total {
		total[index] = byte('a' + index%26)
	}
	parts := []Part{
		{Index: 0, SizeBytes: 10, ObjectRef: "p0"},
		{Index: 1, SizeBytes: 10, ObjectRef: "p1"},
		{Index: 2, SizeBytes: 5, ObjectRef: "p2"},
	}
	for _, part := range parts {
		if _, _, err := backend.Put(ctx, part.ObjectRef,
			bytes.NewReader(total[part.Index*10:part.Index*10+int32(part.SizeBytes)])); err != nil {
			t.Fatalf("准备分片失败：%v", err)
		}
	}
	reader := newPartsReader(ctx, backend, parts, 0, "")

	expectRange := func(name string, seekTo int64, whence int, length int) {
		t.Helper()
		if _, err := reader.Seek(seekTo, whence); err != nil {
			t.Fatalf("%s：Seek 失败：%v", name, err)
		}
		buf := make([]byte, length)
		n, err := io.ReadFull(reader, buf)
		if err != nil {
			t.Fatalf("%s：读取失败（读 %d 字节）：%v", name, n, err)
		}
		// 计算期望内容：Seek 后的绝对位置 = SeekStart 时是 seekTo，
		// SeekEnd 时是 len(total)+seekTo。
		position := seekTo
		if whence == io.SeekEnd {
			position = int64(len(total)) + seekTo
		}
		if !bytes.Equal(buf, total[position:int64(position)+int64(length)]) {
			t.Fatalf("%s：内容错位\n期望 %q\n实际 %q",
				name, total[position:position+int64(length)], buf)
		}
	}

	// 1) 片首（此前侥幸正确的场景）。
	expectRange("片首 p0", 0, io.SeekStart, 4)
	expectRange("片首 p1（片边界）", 10, io.SeekStart, 4)
	// 2) 片内偏移：首次进入某片时必须从片内偏移开始读。
	expectRange("p1 片内偏移", 12, io.SeekStart, 4)
	expectRange("p0 片内偏移", 3, io.SeekStart, 4)
	// 3) 同片内连续跳转（底层位置必须跟随逻辑位置）。
	expectRange("p0 片内跳转", 6, io.SeekStart, 2)
	expectRange("p0 片内回跳", 1, io.SeekStart, 2)
	// 4) 跨片读取：9 → 14 跨 p0/p1 边界。
	expectRange("跨片读取", 9, io.SeekStart, 5)
	// 5) 末尾读取（suffix range 的等价路径）。
	expectRange("末尾读取", -3, io.SeekEnd, 3)
	// 6) 跨片跳转后再回跳早期片。
	expectRange("跳末尾再回跳", 20, io.SeekStart, 2)
	expectRange("回跳 p0", 2, io.SeekStart, 2)

	// 7) 全量顺序读必须与原文一致（回归基线）。
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("全量读 Seek 失败：%v", err)
	}
	all, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("全量读失败：%v", err)
	}
	if !bytes.Equal(all, total) {
		t.Fatalf("全量读内容不一致：%q", all)
	}
}
