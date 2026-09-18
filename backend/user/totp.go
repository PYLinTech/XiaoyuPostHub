package user

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"

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

func (r *Repo) SaveTOTP(ctx context.Context, userID int64, secret, code string) error {
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

func (r *Repo) ConsumeTOTPChallenge(ctx context.Context, token, code string) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tokenHash := randomtoken.Hash(token)
	var userID int64
	var secret *string
	var failedAttempts int
	// FOR UPDATE 锁定挑战行让并发校验串行化，否则多个并发请求可能同时
	// 读到旧计数，绕过尝试次数上限。
	err = tx.QueryRow(ctx, `SELECT c.user_id,u.totp_secret,c.failed_attempts
		FROM login_totp_challenges c
		JOIN users u ON u.id=c.user_id
		WHERE c.token_hash=$1 AND c.expires_at>NOW()
		FOR UPDATE OF c`, tokenHash).Scan(&userID, &secret, &failedAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrTOTPInvalid
	}
	if err != nil {
		return 0, fmt.Errorf("load challenge: %w", err)
	}

	if secret == nil || !ValidateTOTP(*secret, code) {
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
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return userID, nil
}
