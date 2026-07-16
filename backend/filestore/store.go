// Package filestore 负责资源内容在磁盘上的安全落盘、校验和打包。
package filestore

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
)

var (
	ErrChecksumMismatch = errors.New("filestore: 文件校验失败")
	ErrUnsafeStorageKey = errors.New("filestore: 非法存储键")
)

type Store struct {
	settings *systemsetting.Repo
}

func New(settings *systemsetting.Repo) *Store { return &Store{settings: settings} }

func (s *Store) Root(ctx context.Context) (string, error) {
	settings, err := s.settings.Get(ctx)
	if err != nil {
		return "", err
	}
	root := filepath.Clean(settings.StoragePath)
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: %s", ErrUnsafeStorageKey, root)
	}
	if err := os.MkdirAll(filepath.Join(root, ".tmp"), 0o750); err != nil {
		return "", fmt.Errorf("创建存储目录: %w", err)
	}
	return root, nil
}

func (s *Store) NewTemp(ctx context.Context, pattern string) (*os.File, error) {
	root, err := s.Root(ctx)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Join(root, ".tmp"), pattern)
	if err != nil {
		return nil, fmt.Errorf("创建临时文件: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

func (s *Store) Commit(ctx context.Context, tempPath, storageKey string) (string, error) {
	root, err := s.Root(ctx)
	if err != nil {
		return "", err
	}
	finalPath, err := safePath(root, storageKey)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o750); err != nil {
		return "", fmt.Errorf("创建文件目录: %w", err)
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return "", fmt.Errorf("提交文件: %w", err)
	}
	return finalPath, nil
}

func (s *Store) Path(ctx context.Context, storageKey string) (string, error) {
	root, err := s.Root(ctx)
	if err != nil {
		return "", err
	}
	return safePath(root, storageKey)
}

// Remove 删除一个已校验存储键对应的物理文件。文件不存在视为已完成清理。
func (s *Store) Remove(ctx context.Context, storageKey string) error {
	path, err := s.Path(ctx, storageKey)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) ValidateFile(ctx context.Context, item resource.Resource) (string, error) {
	if item.Kind != resource.KindFile || item.StorageKey == nil || item.SHA256Checksum == nil {
		return "", fmt.Errorf("filestore: 资源不是完整文件")
	}
	filePath, err := s.Path(ctx, *item.StorageKey)
	if err != nil {
		return "", err
	}
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	checksum, size, err := checksumReader(f)
	if err != nil {
		return "", err
	}
	if checksum != *item.SHA256Checksum || size != item.SizeBytes {
		return "", ErrChecksumMismatch
	}
	return filePath, nil
}

// BuildZip 递归打包目录。所有文件在 ZIP 完成前逐一重新计算 SHA-256，任何一个
// 文件损坏都会中止，不会把部分损坏内容发送给下载方。
func (s *Store) BuildZip(ctx context.Context, tree []resource.TreeEntry) (string, int64, error) {
	if len(tree) == 0 || tree[0].Kind != resource.KindFolder {
		return "", 0, fmt.Errorf("filestore: ZIP 根资源必须是文件夹")
	}
	temp, err := s.NewTemp(ctx, "folder-*.zip")
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
		if entry.StorageKey == nil || entry.SHA256Checksum == nil {
			return "", 0, fmt.Errorf("filestore: 文件元数据不完整: %s", entry.ID)
		}
		filePath, err := s.Path(ctx, *entry.StorageKey)
		if err != nil {
			return "", 0, err
		}
		src, err := os.Open(filePath)
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
			return "", 0, fmt.Errorf("%w: %s", ErrChecksumMismatch, entry.RelativePath)
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

func ChecksumFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return checksumReader(f)
}

func checksumReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func safePath(root, storageKey string) (string, error) {
	if storageKey == "" || filepath.IsAbs(storageKey) {
		return "", ErrUnsafeStorageKey
	}
	clean := filepath.Clean(storageKey)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrUnsafeStorageKey
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrUnsafeStorageKey
	}
	return full, nil
}
