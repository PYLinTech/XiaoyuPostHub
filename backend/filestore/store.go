// Package filestore 负责上传分片的临时落盘与校验工具。
//
// 说明：物理对象（blob）的读写已迁移到 blobstore；本包只保留上传过程中
// 「尚未成为正式对象」的临时分片管理，以及制品文件的校验工具。
package filestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
)

var (
	ErrUnsafeStorageKey = errors.New("filestore: 非法存储键")
	// ErrChunkTooLarge 表示上传分片超过声明上限（调用方据此返回 413 而非 500）。
	ErrChunkTooLarge = errors.New("filestore: 上传分片超过允许大小")
)

type Store struct {
	settings *systemsetting.Repo
}

func New(settings *systemsetting.Repo) *Store { return &Store{settings: settings} }

// Root 返回存储根目录（绝对路径，自动创建 .tmp 子目录）。管理员磁盘统计与
// 上传临时目录都基于它。
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

// WriteUploadChunk 原子写入一个上传分片。目录名只能使用服务端生成的会话 token。
func (s *Store) WriteUploadChunk(ctx context.Context, sessionID string, index int32, src io.Reader, limit int64) (string, string, int32, error) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\\.`) || index < 0 {
		return "", "", 0, ErrUnsafeStorageKey
	}
	root, err := s.Root(ctx)
	if err != nil {
		return "", "", 0, err
	}
	relativePath := filepath.ToSlash(filepath.Join("upload-sessions", sessionID, fmt.Sprintf("%d.part", index)))
	dir := filepath.Join(root, ".tmp", "upload-sessions", sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", 0, err
	}
	temp, err := os.CreateTemp(dir, "chunk-*")
	if err != nil {
		return "", "", 0, err
	}
	tempPath := temp.Name()
	ok := false
	defer func() {
		_ = temp.Close()
		if !ok {
			_ = os.Remove(tempPath)
		}
	}()
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(src, limit+1))
	if copyErr != nil || n > limit {
		if copyErr == nil {
			copyErr = ErrChunkTooLarge
		}
		return "", "", 0, copyErr
	}
	if err := temp.Sync(); err != nil {
		return "", "", 0, err
	}
	if err := temp.Close(); err != nil {
		return "", "", 0, err
	}
	finalPath := filepath.Join(dir, fmt.Sprintf("%d.part", index))
	if err := os.Rename(tempPath, finalPath); err != nil {
		return "", "", 0, err
	}
	ok = true
	return relativePath, hex.EncodeToString(hash.Sum(nil)), int32(n), nil
}

func (s *Store) UploadChunkPath(ctx context.Context, relativePath string) (string, error) {
	root, err := s.Root(ctx)
	if err != nil {
		return "", err
	}
	return safePath(filepath.Join(root, ".tmp"), relativePath)
}

func (s *Store) RemoveUploadSession(ctx context.Context, sessionID string) error {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\\.`) {
		return ErrUnsafeStorageKey
	}
	root, err := s.Root(ctx)
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(root, ".tmp", "upload-sessions", sessionID))
}

// ChecksumFile 计算本地文件的 SHA-256 与大小（用于下载制品校验）。
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

func safePath(root, ref string) (string, error) {
	if ref == "" || filepath.IsAbs(ref) {
		return "", ErrUnsafeStorageKey
	}
	clean := filepath.Clean(ref)
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
