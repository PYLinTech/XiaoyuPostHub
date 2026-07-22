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

func (r *Repo) ConsumeTOTPChallenge(ctx context.Context, token, code string) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var userID int64
	var secret *string
	err = tx.QueryRow(ctx, `DELETE FROM login_totp_challenges c USING users u
		WHERE c.token_hash=$1 AND c.expires_at>NOW() AND u.id=c.user_id
		RETURNING c.user_id,u.totp_secret`, randomtoken.Hash(token)).Scan(&userID, &secret)
	if errors.Is(err, pgx.ErrNoRows) || secret == nil || !ValidateTOTP(*secret, code) {
		return 0, ErrTOTPInvalid
	}
	if err != nil {
		return 0, fmt.Errorf("consume challenge: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return userID, nil
}
