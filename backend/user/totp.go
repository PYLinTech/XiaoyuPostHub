package user

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp/totp"
)

var ErrTOTPInvalid = errors.New("动态令牌无效")

type TOTPSetup struct {
	Secret string `json:"secret"`
	URL    string `json:"url"`
	QRCode string `json:"qrCode"`
}

func (r *Repo) BeginTOTPSetup(ctx context.Context, userID int64, issuer, account string) (TOTPSetup, error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: account})
	if err != nil {
		return TOTPSetup{}, err
	}
	image, err := key.Image(256, 256)
	if err != nil {
		return TOTPSetup{}, err
	}
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image); err != nil {
		return TOTPSetup{}, err
	}
	return TOTPSetup{Secret: key.Secret(), URL: key.URL(), QRCode: "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes.Bytes())}, nil
}

func ValidateTOTP(secret, code string) bool { return totp.Validate(code, secret) }

const totpPeriodSeconds = 30

// validateTOTPStep 校验动态码并返回匹配的时间步长（-1 表示无匹配）。窗口与
// pquerna/otp 的 Validate 默认一致（当前步 ±1）。调用方据此拒绝"已使用过的
// 步长"，防止同一验证码在有效窗口内被重放换取新登录会话。
func validateTOTPStep(secret, code string, at time.Time) int64 {
	code = strings.TrimSpace(code)
	for offset := int64(1); offset >= -1; offset-- {
		step := at.Unix()/totpPeriodSeconds + offset
		expected, err := totp.GenerateCode(secret, time.Unix(step*totpPeriodSeconds, 0))
		if err == nil && subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return step
		}
	}
	return -1
}

// SaveTOTP 绑定/替换动态令牌密钥。替换已配置的令牌时必须验证当前令牌：
// 否则会话被劫持后攻击者可直接把 2FA 换成自己的密钥，形成长期后门。
func (r *Repo) SaveTOTP(ctx context.Context, userID int64, secret, code, currentCode string) error {
	var existing *string
	var lastStep int64
	if err := r.pool.QueryRow(ctx, `SELECT totp_secret,last_totp_step FROM users WHERE id=$1`, userID).Scan(&existing, &lastStep); err != nil {
		return err
	}
	if existing != nil && *existing != "" {
		step := validateTOTPStep(*existing, currentCode, time.Now())
		if step < 0 || step <= lastStep {
			return ErrTOTPInvalid
		}
	}
	if !ValidateTOTP(secret, code) {
		return ErrTOTPInvalid
	}
	_, err := r.pool.Exec(ctx, `UPDATE users SET totp_secret=$2,totp_grace_used=TRUE WHERE id=$1`, userID, secret)
	return err
}

func (r *Repo) MarkTOTPGraceUsed(ctx context.Context, userID int64) error {
	_, err := r.pool.Exec(ctx, `UPDATE users SET totp_grace_used=TRUE WHERE id=$1`, userID)
	return err
}

func (r *Repo) CreateTOTPChallenge(ctx context.Context, userID int64) (string, error) {
	token, err := randomtoken.New(32)
	if err != nil {
		return "", err
	}
	_, err = r.pool.Exec(ctx, `WITH cleanup AS (
		DELETE FROM login_totp_challenges WHERE expires_at<=NOW()
	) INSERT INTO login_totp_challenges(token_hash,user_id,expires_at)
	VALUES($1,$2,NOW()+INTERVAL '5 minutes')`, randomtoken.Hash(token), userID)
	return token, err
}

// MaxTOTPChallengeAttempts 是单个登录挑战允许的验证码失败次数。
// 达到上限后挑战立即作废，用户需要重新用密码换取新挑战，避免在 5 分钟
// 有效期内对 6 位验证码做无限次暴力尝试。
const MaxTOTPChallengeAttempts = 5

func (r *Repo) ConsumeTOTPChallenge(ctx context.Context, token, code, clientIP string) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tokenHash := randomtoken.Hash(token)
	var userID int64
	var username string
	var secret *string
	var lastStep int64
	var failedAttempts int
	// FOR UPDATE 锁定挑战行让并发校验串行化，否则多个并发请求可能同时
	// 读到旧计数，绕过尝试次数上限。
	err = tx.QueryRow(ctx, `SELECT c.user_id,u.username,u.totp_secret,u.last_totp_step,c.failed_attempts
		FROM login_totp_challenges c
		JOIN users u ON u.id=c.user_id
		WHERE c.token_hash=$1 AND c.expires_at>NOW()
		FOR UPDATE OF c`, tokenHash).Scan(&userID, &username, &secret, &lastStep, &failedAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrTOTPInvalid
	}
	if err != nil {
		return 0, fmt.Errorf("load challenge: %w", err)
	}

	step := int64(-1)
	if secret != nil {
		step = validateTOTPStep(*secret, code, time.Now())
	}
	// 重放防护：命中的时间步长必须大于上次成功使用的步长；否则同一验证码可在
	// 有效窗口内被重复使用。与"无效"同口径返回，避免给攻击者探测信息。
	replayed := step >= 0 && step <= lastStep
	if secret == nil || step < 0 || replayed {
		// 动态令牌失败同样计入登录失败记录：否则在密码已知的前提下可以不断
		// 新建挑战、每次试 5 个码，绕过账号维度的失败限流。
		if _, err := tx.Exec(ctx, `INSERT INTO login_failure_events(account_key, client_ip) VALUES($1,$2)`,
			strings.ToLower(username), clientIP); err != nil {
			return 0, fmt.Errorf("record totp failure: %w", err)
		}
		failedAttempts++
		if failedAttempts >= MaxTOTPChallengeAttempts {
			if _, err := tx.Exec(ctx, `DELETE FROM login_totp_challenges WHERE token_hash=$1`, tokenHash); err != nil {
				return 0, fmt.Errorf("drop exhausted challenge: %w", err)
			}
		} else if _, err := tx.Exec(ctx, `UPDATE login_totp_challenges SET failed_attempts=$2 WHERE token_hash=$1`, tokenHash, failedAttempts); err != nil {
			return 0, fmt.Errorf("record challenge attempt: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		return 0, ErrTOTPInvalid
	}

	if _, err := tx.Exec(ctx, `DELETE FROM login_totp_challenges WHERE token_hash=$1`, tokenHash); err != nil {
		return 0, fmt.Errorf("consume challenge: %w", err)
	}
	// 记录本次使用的时间步长：同一验证码在窗口内再次提交将被拒绝。
	if _, err := tx.Exec(ctx, `UPDATE users SET last_totp_step=$2 WHERE id=$1`, userID, step); err != nil {
		return 0, fmt.Errorf("record totp step: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return userID, nil
}
