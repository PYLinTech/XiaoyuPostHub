package server

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
)

// buildZip 把资源树打包为本地临时 ZIP 制品。
//
// 所有文件在写入 ZIP 前逐一从对象存储读取并重新计算 SHA-256，任何一个文件
// 损坏都会中止，不会把部分损坏内容发送给下载方（与旧 filestore.BuildZip 语义
// 保持一致；制品路径用于后续一次性交付与临时链接）。
func buildZip(ctx context.Context, deps Deps, tree []resource.TreeEntry) (string, int64, error) {
	if len(tree) == 0 || tree[0].Kind != resource.KindFolder {
		return "", 0, fmt.Errorf("打包根资源必须是文件夹")
	}
	temp, err := deps.FileStore.NewTemp(ctx, "folder-*.zip")
	if err != nil {
		return "", 0, err
	}
	tempPath := temp.Name()
	ok := false
	defer func() {
		_ = temp.Close()
		if !ok {
			_ = os.Remove(tempPath)
		}
	}()

	zw := zip.NewWriter(temp)
	for _, entry := range tree {
		zipName := filepath.ToSlash(entry.RelativePath)
		if entry.Kind == resource.KindFolder {
			if !strings.HasSuffix(zipName, "/") {
				zipName += "/"
			}
			if _, err := zw.CreateHeader(&zip.FileHeader{Name: zipName, Method: zip.Store}); err != nil {
				return "", 0, err
			}
			continue
		}
		if entry.BlobID == nil || entry.SHA256Checksum == nil {
			return "", 0, fmt.Errorf("文件元数据不完整: %s", entry.ID)
		}
		blob, err := deps.Blobs.Get(ctx, *entry.BlobID)
		if err != nil {
			return "", 0, err
		}
		src, err := deps.Blobs.Open(ctx, blob)
		if err != nil {
			return "", 0, err
		}
		h := &zip.FileHeader{Name: zipName, Method: zip.Deflate}
		h.SetModTime(entry.UpdatedAt)
		dst, err := zw.CreateHeader(h)
		if err != nil {
			_ = src.Close()
			return "", 0, err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(dst, hash), src)
		closeErr := src.Close()
		if copyErr != nil {
			return "", 0, copyErr
		}
		if closeErr != nil {
			return "", 0, closeErr
		}
		if n != entry.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != *entry.SHA256Checksum {
			return "", 0, fmt.Errorf("%w: %s", blobstore.ErrChecksumMismatch, entry.RelativePath)
		}
	}
	if err := zw.Close(); err != nil {
		return "", 0, err
	}
	if err := temp.Sync(); err != nil {
		return "", 0, err
	}
	info, err := temp.Stat()
	if err != nil {
		return "", 0, err
	}
	if err := temp.Close(); err != nil {
		return "", 0, err
	}
	ok = true
	return tempPath, info.Size(), nil
}
