package user

import (
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// PasswordCost 是项目统一的 bcrypt cost 因子。
//
// 固定为 12：
//   - 兼顾登录请求时延（cost=12 在常规硬件 ~250ms/次）
//   - 强制所有历史/未来 hash 必须用同一 cost，避免混用导致强度不一致
//
// 任何 path 上生成的 password_hash 都必须是 cost=12。
// VerifyPassword 在比对前会先用 ValidatePasswordHash 校验 cost。
const PasswordCost = 12

// 账号与密码的长度边界，按 Unicode 字符（码点）计数：中文等 UTF-8 字符与
// ASCII 字符同等对待，一个汉字算一位。
//
// 密码上限固定 18 位：18 个四字节字符最多 72 字节，恰好落在 bcrypt 的
// 72 字节硬限制内，任何合法密码都不会被 bcrypt 拒绝。
const (
	UsernameMinChars = 3
	UsernameMaxChars = 18
	PasswordMinChars = 8
	PasswordMaxChars = 18
)

var (
	ErrUsernameLength  = errors.New("user: 账号长度需为 3 至 18 位")
	ErrUsernameCharset = errors.New("user: 账号不能包含空白或控制字符")
	ErrPasswordLength  = errors.New("user: 密码长度需为 8 至 18 位")
	ErrPasswordTooWeak = errors.New("user: 密码太简单")
)

// CharCount 按 Unicode 字符（码点）计数，是账号与密码长度的唯一计数方式。
func CharCount(value string) int { return utf8.RuneCountInString(value) }

// ValidateUsername 校验账号：3 至 18 位，允许中文等任意可见 UTF-8 字符，
// 但不允许空白与控制字符（避免同形账号与日志注入）。
func ValidateUsername(name string) error {
	if count := CharCount(name); count < UsernameMinChars || count > UsernameMaxChars {
		return ErrUsernameLength
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ErrUsernameCharset
		}
	}
	return nil
}

// ValidateNewPassword 校验新设置的密码：8 至 18 位，且至少包含小写字母、
// 大写字母、数字、其他字符（符号、中文等）中的两类。
//
// 登录路径不调用本函数：历史密码可能不满足当前规则，登录只做宽松的长度
// 上限检查，避免把老账号锁在门外。
func ValidateNewPassword(password string) error {
	if count := CharCount(password); count < PasswordMinChars || count > PasswordMaxChars {
		return ErrPasswordLength
	}
	if !PasswordMeetsStrength(password) {
		return ErrPasswordTooWeak
	}
	return nil
}

// PasswordMeetsStrength 判断密码是否至少包含两类字符。
//
// 分类固定为 ASCII 语义（小写 a-z / 大写 A-Z / 数字 0-9 / 其他），与前端
// credentialPolicy.ts 逐字对应：中文、符号、带重音字母等一律计入"其他"，
// 避免两端对同一密码得出不同结论。
func PasswordMeetsStrength(password string) bool {
	var lower, upper, digit, other bool
	for _, r := range password {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			other = true
		}
	}
	classes := 0
	for _, present := range [...]bool{lower, upper, digit, other} {
		if present {
			classes++
		}
	}
	return classes >= 2
}

// HashPassword 用 bcrypt cost=12 生成 password_hash。
//
//   - 返回完整 bcrypt 字符串（含 $2a$12$ 前缀），直接存 users.password_hash
//   - 不需要单独的 salt 字段（bcrypt 内部自带 salt）
//   - bcrypt 只接受不超过 72 字节的密码；长度规则（最长 18 位）已保证不会
//     触发，这里保留一道防御，让绕过校验的调用点拿到明确错误
func HashPassword(password string) (string, error) {
	if len(password) > 72 {
		return "", fmt.Errorf("hash password: %w", bcrypt.ErrPasswordTooLong)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), PasswordCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// ValidatePasswordHash 校验 hash 必须是 bcrypt 且 cost=12。
//
//   - 不是 bcrypt 格式 → error
//   - cost != 12 → error
//
// 用于：
//   - BootstrapSuperAdmin 加载 .env 中的 SUPER_ADMIN_PASSWORD_HASH 时
//   - VerifyPassword 比对前的前置校验（拒绝"格式不合法"的 hash）
func ValidatePasswordHash(passwordHash string) error {
	cost, err := bcrypt.Cost([]byte(passwordHash))
	if err != nil {
		return fmt.Errorf("password hash must be bcrypt: %w", err)
	}
	if cost != PasswordCost {
		return fmt.Errorf("password hash bcrypt cost must be %d, got %d", PasswordCost, cost)
	}
	return nil
}

// VerifyPassword 验证明文密码与 password_hash 是否匹配。
//
// 流程：
//  1. 先用 ValidatePasswordHash 校验 hash 格式（cost=12）
//  2. 再用 bcrypt.CompareHashAndPassword 比对
//
// hash 不是 bcrypt / cost 不是 12 → 一律 false（不抛错），调用方用单一
// "账号或密码错误"回复，避免向攻击者泄露 hash 格式信息。
func VerifyPassword(password string, passwordHash string) bool {
	if err := ValidatePasswordHash(passwordHash); err != nil {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) == nil
}
