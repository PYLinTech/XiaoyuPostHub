/**
 * 账号与密码的输入规则，与后端 backend/user/password.go 一一对应：
 * 长度按 Unicode 字符（码点）计数，一个汉字算一位，与 ASCII 字符同等对待。
 */

export const USERNAME_MIN_CHARS = 3;
export const USERNAME_MAX_CHARS = 18;
export const PASSWORD_MIN_CHARS = 8;
export const PASSWORD_MAX_CHARS = 18;

/** 按 Unicode 字符（码点）计数，与后端的 rune 计数保持一致。 */
export function charCount(value: string): number {
  return Array.from(value).length;
}

/**
 * 密码强度：至少包含小写字母、大写字母、数字、其他字符四类中的两类。
 *
 * 分类固定为 ASCII 语义（a-z / A-Z / 0-9 / 其他），与后端
 * user.PasswordMeetsStrength 逐字对应：中文、符号、带重音字母等一律计入
 * "其他"，避免两端对同一密码得出不同结论。
 */
export function passwordMeetsStrength(value: string): boolean {
  let lower = false;
  let upper = false;
  let digit = false;
  let other = false;
  for (const char of value) {
    if (char >= 'a' && char <= 'z') {
      lower = true;
    } else if (char >= 'A' && char <= 'Z') {
      upper = true;
    } else if (char >= '0' && char <= '9') {
      digit = true;
    } else {
      other = true;
    }
  }
  return [lower, upper, digit, other].filter(Boolean).length >= 2;
}
