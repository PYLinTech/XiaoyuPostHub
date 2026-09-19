package blobstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// 分块加密设计（与前端 fileCrypto.ts 严格对称）：
//
//	明文按固定块大小切分，每块独立 AES-256-GCM 加密：
//	  密文 = 块1密文 ‖ 块2密文 ‖ ...   （每块密文 = 明文 + 16 字节 tag）
//	  块 nonce（12 字节）= 文件级 nonce（8 字节）‖ 块序号（4 字节，大端）
//
// 固定块大小让"明文偏移 ↔ 密文偏移"可以直接换算，因此 Range、预览与断点
// 续传在密文对象上全部可用；每文件的随机 DEK 保证同内容不会产生相同密文。
//
// 密钥层次（密钥实体只存在于数据库，KEK 只是部署侧保护层）：
//
//	KEK（.env，可多版本轮换）
//	  └─ 包裹（AES-GCM）每个文件随机生成的 DEK → encryption_dek 列
const (
	// AlgoAESGCMChunked 是本系统唯一的分块加密算法标识。
	AlgoAESGCMChunked = "aes-256-gcm-chunked"
	// EncryptionChunkSize 是加密块大小（4 MiB）。存储分片大小必须是它的整数倍，
	// 这样分片边界与加密块边界对齐，密文流可以跨片连续解密。
	EncryptionChunkSize = int64(4 << 20)
	// EncryptionTagOverhead 是每个加密块的 GCM tag 字节数（用于密文↔明文坐标换算）。
	EncryptionTagOverhead = int64(16)

	dekSize         = 32
	gcmTagSize      = 16
	fileNonceSize   = 8
	wrapNonceSize   = 12
	keyEnvelopeSize = wrapNonceSize + dekSize + gcmTagSize
)

var (
	ErrEncryptionUnavailable = errors.New("blobstore: 未配置加密密钥")
	ErrEncryptionKeyUnknown  = errors.New("blobstore: 加密密钥不存在")
	ErrEncryptionInvalid     = errors.New("blobstore: 加密数据损坏")
	ErrEncryptionPartAlign   = errors.New("blobstore: 分片大小必须是加密块的整数倍")
)

// EncryptionConfig 保存 KEK 集合与主密钥（第一个配置项）。
type EncryptionConfig struct {
	keys    map[string][]byte
	primary string
}

// Available 表示是否可以加密新文件。
func (c *EncryptionConfig) Available() bool { return c != nil && c.primary != "" }

// PrimaryKeyID 返回新文件使用的 keyId（未配置时为空）。
func (c *EncryptionConfig) PrimaryKeyID() string {
	if c == nil {
		return ""
	}
	return c.primary
}

// KeyCount 返回已配置的 KEK 数量（用于启动日志）。
func (c *EncryptionConfig) KeyCount() int {
	if c == nil {
		return 0
	}
	return len(c.keys)
}

// ParseEncryptionKeys 解析 XPH_ENCRYPTION_KEYS=keyId:base64,keyId2:base64。
// 第一个条目用于加密新文件，其余条目用于解密历史数据（密钥轮换）。
// 未配置时返回空配置（加密开关不可用，但不影响明文功能）。
func ParseEncryptionKeys(raw string) (*EncryptionConfig, error) {
	config := &EncryptionConfig{keys: map[string][]byte{}}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		keyID, encoded, ok := strings.Cut(entry, ":")
		keyID = strings.TrimSpace(keyID)
		encoded = strings.TrimSpace(encoded)
		if !ok || keyID == "" || encoded == "" {
			return nil, fmt.Errorf("blobstore: 加密密钥格式应为 keyId:base64（keyId 只是标签）")
		}
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			// 常见错法：hex、URL-safe base64、删掉末尾 = 填充。
			return nil, fmt.Errorf("blobstore: 加密密钥 %q 不是标准 base64（须保留末尾 = 填充）", keyID)
		}
		if len(key) != dekSize {
			// openssl rand -base64 32 → 32 字节；-hex 32 → 48 字节。
			return nil, fmt.Errorf("blobstore: 加密密钥 %q 解码后 %d 字节，应为 %d 字节（openssl rand -base64 32）", keyID, len(key), dekSize)
		}
		if _, exists := config.keys[keyID]; exists {
			return nil, fmt.Errorf("blobstore: 加密密钥 %s 重复", keyID)
		}
		config.keys[keyID] = key
		if config.primary == "" {
			config.primary = keyID
		}
	}
	return config, nil
}

// wrapDEK 用主 KEK 包裹 DEK，输出 wrapNonce ‖ 密文（含 tag）。
func (c *EncryptionConfig) wrapDEK(dek []byte) (string, []byte, error) {
	if !c.Available() {
		return "", nil, ErrEncryptionUnavailable
	}
	aead, err := newGCM(c.keys[c.primary])
	if err != nil {
		return "", nil, err
	}
	nonce := make([]byte, wrapNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, err
	}
	envelope := make([]byte, 0, keyEnvelopeSize)
	envelope = append(envelope, nonce...)
	envelope = aead.Seal(envelope, nonce, dek, nil)
	return c.primary, envelope, nil
}

// unwrapDEK 用指定 keyId 的 KEK 解开 DEK。
func (c *EncryptionConfig) unwrapDEK(keyID string, envelope []byte) ([]byte, error) {
	if c == nil {
		return nil, ErrEncryptionUnavailable
	}
	key, ok := c.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrEncryptionKeyUnknown, keyID)
	}
	if len(envelope) < keyEnvelopeSize {
		return nil, ErrEncryptionInvalid
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := envelope[:wrapNonceSize]
	dek, err := aead.Open(nil, nonce, envelope[wrapNonceSize:], nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncryptionInvalid, err)
	}
	return dek, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// chunkNonce 由文件级 nonce（8 字节）与块序号（4 字节大端）拼接 GCM nonce。
func chunkNonce(fileNonce []byte, index int64) []byte {
	nonce := make([]byte, fileNonceSize+4)
	copy(nonce, fileNonce)
	binary.BigEndian.PutUint32(nonce[fileNonceSize:], uint32(index))
	return nonce
}

// wireSize 返回给定明文长度在分块加密后的密文长度（未加密返回原值）。
func wireSize(plainSize, chunkSize int64) int64 {
	if chunkSize <= 0 {
		return plainSize
	}
	blocks := (plainSize + chunkSize - 1) / chunkSize
	return plainSize + blocks*gcmTagSize
}

// encryptingReader 把明文流转换为密文流（拉模式，供 backend.Put 消费）。
// 完整性校验由调用方基于明文 SHA-256 与写入字节数完成，本类型不额外记账。
type encryptingReader struct {
	src       io.Reader
	aead      cipher.AEAD
	fileNonce []byte
	chunkSize int64
	index     int64
	srcBuf    []byte
	pending   []byte
	pendingAt int
	done      bool
}

func newEncryptingReader(src io.Reader, dek, fileNonce []byte, chunkSize int64) (*encryptingReader, error) {
	aead, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	if chunkSize <= 0 {
		chunkSize = EncryptionChunkSize
	}
	return &encryptingReader{
		src: src, aead: aead, fileNonce: fileNonce, chunkSize: chunkSize,
		srcBuf: make([]byte, chunkSize),
	}, nil
}

func (r *encryptingReader) Read(p []byte) (int, error) {
	for {
		if r.pendingAt < len(r.pending) {
			n := copy(p, r.pending[r.pendingAt:])
			r.pendingAt += n
			return n, nil
		}
		if r.done {
			return 0, io.EOF
		}
		n, err := io.ReadFull(r.src, r.srcBuf)
		if n > 0 {
			nonce := chunkNonce(r.fileNonce, r.index)
			r.pending = r.aead.Seal(r.pending[:0], nonce, r.srcBuf[:n], nil)
			r.pendingAt = 0
			r.index++
		}
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				r.done = true
				if n == 0 {
					return 0, io.EOF
				}
				continue
			}
			return 0, err
		}
	}
}

// encryptedReadSeeker 以块为单位解密，支持 Seek（Range / 预览 / 断点续传）。
// 每次只保留当前解密块（4 MiB），内存占用与文件大小无关。
type encryptedReadSeeker struct {
	reader    io.ReadSeekCloser
	aead      cipher.AEAD
	fileNonce []byte
	chunkSize int64
	size      int64
	pos       int64
	cache     []byte
	cacheFrom int64
}

func newEncryptedReadSeeker(reader io.ReadSeekCloser, dek, fileNonce []byte, size, chunkSize int64) (*encryptedReadSeeker, error) {
	aead, err := newGCM(dek)
	if err != nil {
		_ = reader.Close()
		return nil, err
	}
	if chunkSize <= 0 {
		chunkSize = EncryptionChunkSize
	}
	if len(fileNonce) != fileNonceSize {
		_ = reader.Close()
		return nil, ErrEncryptionInvalid
	}
	return &encryptedReadSeeker{
		reader: reader, aead: aead, fileNonce: fileNonce, chunkSize: chunkSize, size: size,
	}, nil
}

func (r *encryptedReadSeeker) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	if err := r.loadChunk(); err != nil {
		return 0, err
	}
	offset := r.pos - r.cacheFrom
	if offset >= int64(len(r.cache)) {
		return 0, io.EOF
	}
	n := copy(p, r.cache[offset:])
	r.pos += int64(n)
	return n, nil
}

func (r *encryptedReadSeeker) loadChunk() error {
	index := r.pos / r.chunkSize
	from := index * r.chunkSize
	if r.cache != nil && r.cacheFrom == from {
		return nil
	}
	plainLen := r.chunkSize
	if remaining := r.size - from; remaining < plainLen {
		plainLen = remaining
	}
	overhead := int64(r.aead.Overhead())
	wireLen := plainLen + overhead
	if _, err := r.reader.Seek(index*(r.chunkSize+overhead), io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, wireLen)
	if _, err := io.ReadFull(r.reader, buf); err != nil {
		return err
	}
	plain, err := r.aead.Open(buf[:0], chunkNonce(r.fileNonce, index), buf, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEncryptionInvalid, err)
	}
	r.cache = plain
	r.cacheFrom = from
	return nil
}

func (r *encryptedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	target, err := seekTarget(r.pos, r.size, offset, whence)
	if err != nil {
		return 0, err
	}
	r.pos = target
	return target, nil
}

func (r *encryptedReadSeeker) Close() error { return r.reader.Close() }
