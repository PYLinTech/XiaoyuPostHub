package admin

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/inbox"
	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
)

var (
	ErrInvitationTargetInvalid = errors.New("invitation: 发放目标无效")
	ErrInvitationQuantity      = errors.New("invitation: 发放数量必须在 1 到 100 之间")
	ErrInvitationNotAvailable  = errors.New("invitation: 邀请码不存在或已不可用")
)

type InvitationTarget struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type InvitationItem struct {
	ID         int64      `json:"id"`
	CodePrefix string     `json:"codePrefix"`
	TargetType string     `json:"targetType"`
	TargetID   int64      `json:"targetId"`
	TargetName string     `json:"targetName"`
	Status     string     `json:"status"`
	UsedBy     *string    `json:"usedBy,omitempty"`
	UsedAt     *time.Time `json:"usedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type InvitationDashboard struct {
	RegistrationRequiresInvitation bool               `json:"registrationRequiresInvitation"`
	Users                          []InvitationTarget `json:"users"`
	Groups                         []InvitationTarget `json:"groups"`
	Items                          []InvitationItem   `json:"items"`
}

func (r *Repo) GetInvitationDashboard(ctx context.Context) (InvitationDashboard, error) {
	out := InvitationDashboard{
		Users:  make([]InvitationTarget, 0),
		Groups: make([]InvitationTarget, 0),
		Items:  make([]InvitationItem, 0),
	}
	if err := r.pool.QueryRow(ctx, `SELECT registration_requires_invitation FROM system_settings WHERE id=1`).Scan(&out.RegistrationRequiresInvitation); err != nil {
		return out, err
	}
	userRows, err := r.pool.Query(ctx, `SELECT id,username FROM users ORDER BY username`)
	if err != nil {
		return out, err
	}
	for userRows.Next() {
		var item InvitationTarget
		if err := userRows.Scan(&item.ID, &item.Name); err != nil {
			userRows.Close()
			return out, err
		}
		out.Users = append(out.Users, item)
	}
	if err := userRows.Err(); err != nil {
		userRows.Close()
		return out, err
	}
	userRows.Close()

	groupRows, err := r.pool.Query(ctx, `SELECT id,name FROM user_groups ORDER BY name`)
	if err != nil {
		return out, err
	}
	for groupRows.Next() {
		var item InvitationTarget
		if err := groupRows.Scan(&item.ID, &item.Name); err != nil {
			groupRows.Close()
			return out, err
		}
		out.Groups = append(out.Groups, item)
	}
	if err := groupRows.Err(); err != nil {
		groupRows.Close()
		return out, err
	}
	groupRows.Close()

	rows, err := r.pool.Query(ctx, `
		SELECT c.id,c.code_prefix,
		       CASE WHEN c.issued_to_user_id IS NOT NULL THEN 'user' ELSE 'group' END,
		       COALESCE(c.issued_to_user_id,c.issued_to_group_id),
		       COALESCE(target_user.username,target_group.name,'已删除'),
		       CASE WHEN c.revoked_at IS NOT NULL THEN 'revoked' WHEN c.used_at IS NOT NULL THEN 'used' ELSE 'available' END,
		       used_user.username,c.used_at,c.created_at
		FROM invitation_codes c
		LEFT JOIN users target_user ON target_user.id=c.issued_to_user_id
		LEFT JOIN user_groups target_group ON target_group.id=c.issued_to_group_id
		LEFT JOIN users used_user ON used_user.id=c.used_by_user_id
		ORDER BY c.created_at DESC LIMIT 500`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item InvitationItem
		if err := rows.Scan(&item.ID, &item.CodePrefix, &item.TargetType, &item.TargetID, &item.TargetName, &item.Status, &item.UsedBy, &item.UsedAt, &item.CreatedAt); err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}

func (r *Repo) SetRegistrationRequiresInvitation(ctx context.Context, required bool) error {
	_, err := r.pool.Exec(ctx, `UPDATE system_settings SET registration_requires_invitation=$1,updated_at=NOW() WHERE id=1`, required)
	return err
}

func (r *Repo) IssueInvitationCodes(ctx context.Context, actorID int64, targetType string, targetID int64, quantity int) (int64, error) {
	if quantity < 1 || quantity > 100 {
		return 0, ErrInvitationQuantity
	}
	if targetType != "user" && targetType != "group" {
		return 0, ErrInvitationTargetInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	table := "users"
	if targetType == "group" {
		table = "user_groups"
	}
	var exists bool
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE id=$1)`, table), targetID).Scan(&exists); err != nil || !exists {
		return 0, ErrInvitationTargetInvalid
	}
	codes := make([]string, 0, quantity)
	var codeOptions randomtoken.CodeOptions
	if err := tx.QueryRow(ctx, `
		SELECT invitation_length,invitation_case_sensitive,invitation_include_letters,invitation_include_numbers
		FROM system_settings WHERE id=1`).Scan(
		&codeOptions.Length, &codeOptions.CaseSensitive, &codeOptions.IncludeLetters, &codeOptions.IncludeNumbers,
	); err != nil {
		return 0, err
	}
	for len(codes) < quantity {
		code, err := randomtoken.NewCode(codeOptions)
		if err != nil {
			return 0, err
		}
		prefix := code[:4]
		var insertErr error
		if targetType == "user" {
			_, insertErr = tx.Exec(ctx, `INSERT INTO invitation_codes(code_hash,code_prefix,issued_by_user_id,issued_to_user_id) VALUES($1,$2,$3,$4)`, randomtoken.Hash(code), prefix, actorID, targetID)
		} else {
			_, insertErr = tx.Exec(ctx, `INSERT INTO invitation_codes(code_hash,code_prefix,issued_by_user_id,issued_to_group_id) VALUES($1,$2,$3,$4)`, randomtoken.Hash(code), prefix, actorID, targetID)
		}
		if insertErr != nil {
			return 0, insertErr
		}
		codes = append(codes, code)
	}
	var content strings.Builder
	fmt.Fprintf(&content, "<p>系统已向你发放 %d 个邀请码，请妥善保管。</p><div class=\"message-copy-actions\">", quantity)
	for _, code := range codes {
		escapedCode := html.EscapeString(code)
		fmt.Fprintf(
			&content,
			"<button type=\"button\" data-message-action=\"copy\" data-copy-text=\"%s\"><span>复制邀请码</span><code>%s</code></button>",
			escapedCode,
			escapedCode,
		)
	}
	content.WriteString("</div>")
	messageID, err := inbox.InsertTx(
		ctx,
		tx,
		targetType,
		&targetID,
		"邀请码已发放",
		content.String(),
		"邀请码",
	)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return messageID, nil
}

func (r *Repo) RevokeInvitation(ctx context.Context, id int64) error {
	result, err := r.pool.Exec(ctx, `UPDATE invitation_codes SET revoked_at=NOW() WHERE id=$1 AND used_at IS NULL AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrInvitationNotAvailable
	}
	return nil
}
