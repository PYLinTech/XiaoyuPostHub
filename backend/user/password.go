package user

import (
	"fmt"

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

// HashPassword 用 bcrypt cost=12 生成 password_hash。
//
//   - 返回完整 bcrypt 字符串（含 $2a$12$ 前缀），直接存 users.password_hash
//   - 不需要单独的 salt 字段（bcrypt 内部自带 salt）
func HashPassword(password string) (string, error) {
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