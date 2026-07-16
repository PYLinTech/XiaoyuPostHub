// Package randomtoken 生成不可枚举的 URL token 和资源 id。
package randomtoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

const (
	upperLetters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	lowerLetters = "abcdefghijklmnopqrstuvwxyz"
	numbers      = "0123456789"
)

type CodeOptions struct {
	Length         int
	CaseSensitive  bool
	IncludeLetters bool
	IncludeNumbers bool
}

func New(byteLength int) (string, error) {
	if byteLength < 16 {
		return "", fmt.Errorf("randomtoken: byteLength 必须至少为 16")
	}
	b := make([]byte, byteLength)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("randomtoken: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewAlphaNumeric 生成指定长度的大写字母数字随机串。调用方可在校验时统一
// 转成大写，从而提供不区分大小写的输入体验。
func NewAlphaNumeric(length int) (string, error) {
	return NewCode(CodeOptions{Length: length, IncludeLetters: true, IncludeNumbers: true})
}

// NewCode 按给定字符集生成密码学安全的随机码。不区分大小写时仅生成大写
// 字母，校验方可通过 NormalizeCode 提供大小写无关的输入体验。
func NewCode(options CodeOptions) (string, error) {
	if options.Length < 1 {
		return "", fmt.Errorf("randomtoken: length 必须大于 0")
	}
	alphabet := ""
	if options.IncludeLetters {
		alphabet += upperLetters
		if options.CaseSensitive {
			alphabet += lowerLetters
		}
	}
	if options.IncludeNumbers {
		alphabet += numbers
	}
	if alphabet == "" {
		return "", fmt.Errorf("randomtoken: 字母和数字不能同时排除")
	}
	out := make([]byte, options.Length)
	upper := big.NewInt(int64(len(alphabet)))
	for index := range out {
		value, err := rand.Int(rand.Reader, upper)
		if err != nil {
			return "", fmt.Errorf("randomtoken: %w", err)
		}
		out[index] = alphabet[value.Int64()]
	}
	return string(out), nil
}

func NormalizeCode(code string, caseSensitive bool) string {
	code = strings.TrimSpace(code)
	if !caseSensitive {
		code = strings.ToUpper(code)
	}
	return code
}

func ValidCode(code string, options CodeOptions) bool {
	if len(code) != options.Length {
		return false
	}
	for _, char := range code {
		letter := (char >= 'A' && char <= 'Z') || (options.CaseSensitive && char >= 'a' && char <= 'z')
		number := char >= '0' && char <= '9'
		if (letter && options.IncludeLetters) || (number && options.IncludeNumbers) {
			continue
		}
		return false
	}
	return true
}

func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
