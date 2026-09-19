package user

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// 验证码需能解析出"当前时间步长"，并支持相邻一步（时钟偏移窗口）。
func TestValidateTOTPStepMatchesWindow(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "test", AccountName: "user"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0) // 固定时间，避免跨步长边界抖动
	currentStep := now.Unix() / totpPeriodSeconds

	code, err := totp.GenerateCode(key.Secret(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got := validateTOTPStep(key.Secret(), code, now); got != currentStep {
		t.Fatalf("当前验证码应命中步长 %d，得到 %d", currentStep, got)
	}

	prevCode, err := totp.GenerateCode(key.Secret(), time.Unix((currentStep-1)*totpPeriodSeconds, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := validateTOTPStep(key.Secret(), prevCode, now); got != currentStep-1 {
		t.Fatalf("上一步验证码应命中步长 %d，得到 %d", currentStep-1, got)
	}
}

// 无效验证码必须返回 -1（调用方据此拒绝，且与"重放"同口径）。
func TestValidateTOTPStepRejectsInvalidCode(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "test", AccountName: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if got := validateTOTPStep(key.Secret(), "000000", time.Now()); got != -1 {
		t.Fatalf("无效验证码应返回 -1，得到 %d", got)
	}
}
