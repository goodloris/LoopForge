package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// A Feishu grant is a member's consent for the platform to call Feishu as
// them (user_access_token), kept so agents can use the speaker's own Feishu
// identity in conversation workspaces. It is a credential, not a sign-in: a
// password account may hold one without gaining a Feishu login, so the
// connected identity lives here and never in user_identities.
//
// The refresh token is single use (Feishu rotates it on every refresh), so a
// refresh runs under a row lock and writes the new pair back in the same
// transaction; a concurrent round waits on the lock and then reuses the token
// the first one stored. Tokens stay in plaintext like every other stored
// credential (column encryption is tracked separately) and never leave the
// platform except as the short-lived access token injected into a round.

// Feishu grant statuses.
const (
	FeishuGrantActive  = "active"
	FeishuGrantExpired = "expired" // refresh rejected: the member must authorize again
	FeishuGrantRevoked = "revoked" // disconnected by the member
)

type userFeishuGrantRow struct {
	UserID           int64      `gorm:"column:user_id;primaryKey;autoIncrement:false"`
	Tenant           string     `gorm:"column:tenant;size:128;not null;default:''"`
	Subject          string     `gorm:"column:subject;size:128;not null;default:''"`
	OpenID           string     `gorm:"column:open_id;size:128;not null;default:''"`
	DisplayName      string     `gorm:"column:display_name;size:128;not null;default:''"`
	Preset           string     `gorm:"column:preset;size:16;not null;default:''"`
	Scopes           string     `gorm:"column:scopes;type:text"`
	RefreshToken     string     `gorm:"column:refresh_token;type:text"`
	RefreshExpiresAt *time.Time `gorm:"column:refresh_expires_at"`
	AccessToken      string     `gorm:"column:access_token;type:text"`
	AccessExpiresAt  *time.Time `gorm:"column:access_expires_at"`
	Status           string     `gorm:"column:status;size:16;not null;default:''"`
	UpdatedAt        *time.Time `gorm:"column:updated_at"`
}

func (userFeishuGrantRow) TableName() string { return "user_feishu_grants" }

// FeishuTokens is one token response from Feishu (authorization code or
// refresh). Zero expiry times mean Feishu did not say.
type FeishuTokens struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	Scopes           []string
}

// FeishuGrantInput is a fresh authorization to store.
type FeishuGrantInput struct {
	Tenant, Subject, OpenID, Name, Preset string
	Tokens                                FeishuTokens
}

// FeishuGrant describes a stored grant without its tokens.
type FeishuGrant struct {
	Status           string
	Tenant           string
	Subject          string
	OpenID           string
	Name             string
	Preset           string
	Scopes           []string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	UpdatedAt        time.Time
}

// FeishuAccess is a user access token ready to inject into one round.
type FeishuAccess struct {
	Token     string
	ExpiresAt time.Time
	Name      string
	Preset    string
	Scopes    []string
}

// ErrFeishuNotConnected means the member has no active grant.
var ErrFeishuNotConnected = errors.New("feishu is not connected")

// ErrFeishuGrantRejected is wrapped by a refresh function when Feishu refuses
// the refresh token itself (expired, revoked, already used, app removed). The
// grant is then marked expired; any other refresh error leaves it untouched.
var ErrFeishuGrantRejected = errors.New("feishu rejected the refresh token")

// FeishuRefreshFunc exchanges a refresh token for a new token pair.
type FeishuRefreshFunc func(ctx context.Context, refreshToken string) (FeishuTokens, error)

// SaveFeishuGrant stores (or replaces) the member's grant as active.
func (r *UsersRepo) SaveFeishuGrant(userID int64, in FeishuGrantInput) error {
	if userID <= 0 || strings.TrimSpace(in.Tokens.AccessToken) == "" || strings.TrimSpace(in.Tokens.RefreshToken) == "" {
		return errors.New("feishu grant needs a user, an access token and a refresh token")
	}
	now := time.Now().UTC()
	row := userFeishuGrantRow{
		UserID:      userID,
		Tenant:      strings.TrimSpace(in.Tenant),
		Subject:     strings.TrimSpace(in.Subject),
		OpenID:      strings.TrimSpace(in.OpenID),
		DisplayName: strings.TrimSpace(in.Name),
		Preset:      strings.TrimSpace(in.Preset),
		Status:      FeishuGrantActive,
		UpdatedAt:   &now,
	}
	applyFeishuTokens(&row, in.Tokens)
	return r.DB.Save(&row).Error
}

// FeishuGrant returns the member's grant, if any.
func (r *UsersRepo) FeishuGrant(userID int64) (FeishuGrant, bool) {
	var row userFeishuGrantRow
	res := r.DB.Where("user_id = ?", userID).Limit(1).Find(&row)
	if res.Error != nil || res.RowsAffected == 0 {
		return FeishuGrant{}, false
	}
	return FeishuGrant{
		Status:           row.Status,
		Tenant:           row.Tenant,
		Subject:          row.Subject,
		OpenID:           row.OpenID,
		Name:             row.DisplayName,
		Preset:           row.Preset,
		Scopes:           strings.Fields(row.Scopes),
		AccessExpiresAt:  timeOrZero(row.AccessExpiresAt),
		RefreshExpiresAt: timeOrZero(row.RefreshExpiresAt),
		UpdatedAt:        timeOrZero(row.UpdatedAt),
	}, true
}

// RevokeFeishuGrant clears the member's tokens and marks the grant revoked.
// It returns the refresh token it removed ("" when there was none) so the
// caller can also revoke it at Feishu.
func (r *UsersRepo) RevokeFeishuGrant(userID int64) (string, error) {
	var refresh string
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var row userFeishuGrantRow
		res := r.locking(tx).Where("user_id = ?", userID).Limit(1).Find(&row)
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		refresh = row.RefreshToken
		return tx.Model(&userFeishuGrantRow{}).Where("user_id = ?", userID).Updates(clearedFeishuTokens(FeishuGrantRevoked)).Error
	})
	return refresh, err
}

// FeishuUserToken returns an access token with at least minValid left,
// refreshing it when it would expire sooner. Concurrent callers on any
// instance serialize on the grant row; after the lock the row is read again,
// so only the first refreshes and the rest reuse what it stored. A rejected
// refresh token marks the grant expired and returns ErrFeishuNotConnected; a
// transient refresh failure returns the error and keeps the grant as is.
func (r *UsersRepo) FeishuUserToken(ctx context.Context, userID int64, minValid time.Duration, refresh FeishuRefreshFunc) (FeishuAccess, error) {
	var row userFeishuGrantRow
	res := r.DB.WithContext(ctx).Where("user_id = ?", userID).Limit(1).Find(&row)
	if res.Error != nil {
		return FeishuAccess{}, res.Error
	}
	if res.RowsAffected == 0 || row.Status != FeishuGrantActive {
		return FeishuAccess{}, ErrFeishuNotConnected
	}
	if feishuAccessFresh(row, minValid) {
		return feishuAccessFromRow(row), nil
	}
	var out FeishuAccess
	var rejected bool
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row = userFeishuGrantRow{}
		res := r.locking(tx).Where("user_id = ?", userID).Limit(1).Find(&row)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 || row.Status != FeishuGrantActive {
			return ErrFeishuNotConnected
		}
		if feishuAccessFresh(row, minValid) {
			out = feishuAccessFromRow(row)
			return nil
		}
		tokens, err := refresh(ctx, row.RefreshToken)
		if errors.Is(err, ErrFeishuGrantRejected) {
			rejected = true
			return tx.Model(&userFeishuGrantRow{}).Where("user_id = ?", userID).Updates(clearedFeishuTokens(FeishuGrantExpired)).Error
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(tokens.AccessToken) == "" || strings.TrimSpace(tokens.RefreshToken) == "" {
			return errors.New("feishu refresh returned no token")
		}
		now := time.Now().UTC()
		applyFeishuTokens(&row, tokens)
		row.UpdatedAt = &now
		if err := tx.Model(&userFeishuGrantRow{}).Where("user_id = ?", userID).Updates(map[string]any{
			"access_token":       row.AccessToken,
			"access_expires_at":  row.AccessExpiresAt,
			"refresh_token":      row.RefreshToken,
			"refresh_expires_at": row.RefreshExpiresAt,
			"scopes":             row.Scopes,
			"updated_at":         row.UpdatedAt,
		}).Error; err != nil {
			return err
		}
		out = feishuAccessFromRow(row)
		return nil
	})
	if err != nil {
		return FeishuAccess{}, err
	}
	if rejected {
		return FeishuAccess{}, ErrFeishuNotConnected
	}
	return out, nil
}

func feishuAccessFresh(row userFeishuGrantRow, minValid time.Duration) bool {
	return strings.TrimSpace(row.AccessToken) != "" && row.AccessExpiresAt != nil && row.AccessExpiresAt.After(time.Now().Add(minValid))
}

func feishuAccessFromRow(row userFeishuGrantRow) FeishuAccess {
	return FeishuAccess{Token: row.AccessToken, ExpiresAt: timeOrZero(row.AccessExpiresAt), Name: row.DisplayName, Preset: row.Preset, Scopes: strings.Fields(row.Scopes)}
}

func applyFeishuTokens(row *userFeishuGrantRow, t FeishuTokens) {
	row.AccessToken = strings.TrimSpace(t.AccessToken)
	row.RefreshToken = strings.TrimSpace(t.RefreshToken)
	row.AccessExpiresAt = utcTimePtr(t.AccessExpiresAt)
	row.RefreshExpiresAt = utcTimePtr(t.RefreshExpiresAt)
	if len(t.Scopes) > 0 {
		row.Scopes = strings.Join(t.Scopes, " ")
	}
}

func clearedFeishuTokens(status string) map[string]any {
	return map[string]any{
		"status": status, "access_token": "", "refresh_token": "",
		"access_expires_at": nil, "refresh_expires_at": nil, "updated_at": time.Now().UTC(),
	}
}

func utcTimePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
