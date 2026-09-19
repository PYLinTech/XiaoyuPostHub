package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// RootProvider 返回本机存储根目录（来自 system_settings.storage_path）。
// 只要求绝对路径，不存在时由调用方决定是否创建。
type RootProvider func(ctx context.Context) (string, error)

// LocalBackend 把对象保存为本机存储根目录下的文件（现有实现的原位迁移）。
type LocalBackend struct {
	root RootProvider
}

func NewLocalBackend(root RootProvider) *LocalBackend { return &LocalBackend{root: root} }

func (b *LocalBackend) Kind() string { return "local" }

// Put 原子写入：临时文件 + Sync + Rename，失败不留半成品。
// 本机后端的真实定位符就是 ref 本身。
func (b *LocalBackend) Put(ctx context.Context, ref string, r io.Reader) (int64, string, error) {
	rootPath, err := b.root(ctx)
	if err != nil {
		return 0, "", err
	}
	finalPath, err := safePath(rootPath, ref)
	if err != nil {
		return 0, "", err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o750); err != nil {
		return 0, "", fmt.Errorf("创建对象目录: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(rootPath, ".tmp"), 0o750); err != nil {
		return 0, "", fmt.Errorf("创建临时目录: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Join(rootPath, ".tmp"), "blob-*")
	if err != nil {
		return 0, "", fmt.Errorf("创建临时文件: %w", err)
	}
	tempPath := temp.Name()
	ok := false
	defer func() {
		_ = temp.Close()
		if !ok {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return 0, "", err
	}
	written, err := io.Copy(temp, r)
	if err != nil {
		return 0, "", err
	}
	if err := temp.Sync(); err != nil {
		return 0, "", err
	}
	if err := temp.Close(); err != nil {
		return 0, "", err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return 0, "", fmt.Errorf("提交对象: %w", err)
	}
	ok = true
	return written, ref, nil
}

func (b *LocalBackend) Open(ctx context.Context, ref string) (io.ReadSeekCloser, error) {
	rootPath, err := b.root(ctx)
	if err != nil {
		return nil, err
	}
	fullPath, err := safePath(rootPath, ref)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(fullPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrBlobNotFound
		}
		return nil, err
	}
	return f, nil
}

func (b *LocalBackend) Delete(ctx context.Context, ref string) error {
	rootPath, err := b.root(ctx)
	if err != nil {
		return err
	}
	fullPath, err := safePath(rootPath, ref)
	if err != nil {
		return err
	}
	if err := os.Remove(fullPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (b *LocalBackend) Stat(ctx context.Context, ref string) (int64, error) {
	rootPath, err := b.root(ctx)
	if err != nil {
		return 0, err
	}
	fullPath, err := safePath(rootPath, ref)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, ErrBlobNotFound
		}
		return 0, err
	}
	return info.Size(), nil
}

// safePath 校验对象定位符不越出根目录（从 filestore 迁移，行为保持不变）。
func safePath(root, ref string) (string, error) {
	if ref == "" || filepath.IsAbs(ref) {
		return "", ErrUnsafeRef
	}
	clean := filepath.Clean(ref)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrUnsafeRef
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrUnsafeRef
	}
	return full, nil
}
