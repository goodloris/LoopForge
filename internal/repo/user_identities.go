package repo

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"agentflow/internal/domain"
)

// External sign-in providers (Feishu today) bind accounts through
// user_identities, one row per (provider, tenant, subject). A sign-in finds
// its account only by that key, never by login name, Chinese name or email,
// so it cannot take over a password account. A new provider adds an adapter
// that produces an ExternalIdentity; it needs no new users columns.

// IdentityProviderFeishu is the Feishu (飞书) web login.
const IdentityProviderFeishu = "feishu"

type userIdentityRow struct {
	ID       int64  `gorm:"column:id;primaryKey;autoIncrement"`
	UserID   int64  `gorm:"column:user_id;not null;index:idx_user_identities_user"`
	Provider string `gorm:"column:provider;size:32;not null;uniqueIndex:uq_user_identities,priority:1"`
	// Tenant scopes Subject (Feishu tenant_key); '' for providers without
	// tenants. NOT NULL so the unique key also holds for those.
	Tenant      string     `gorm:"column:tenant;size:128;not null;default:'';uniqueIndex:uq_user_identities,priority:2"`
	Subject     string     `gorm:"column:subject;size:128;not null;uniqueIndex:uq_user_identities,priority:3"`
	DisplayName string     `gorm:"column:display_name;size:128;not null;default:''"`
	AvatarURL   *string    `gorm:"column:avatar_url;type:text"`
	Created     *time.Time `gorm:"column:created_at"`
	LastLogin   *time.Time `gorm:"column:last_login_at"`
}

func (userIdentityRow) TableName() string { return "user_identities" }

// ExternalIdentity is what a sign-in adapter learned about the member.
type ExternalIdentity struct {
	Provider  string
	Tenant    string // Feishu tenant_key; "" when the provider has none
	Subject   string // Feishu union_id, or open_id when the app has none
	Name      string
	AvatarURL string
}

func (id ExternalIdentity) normalized() (ExternalIdentity, error) {
	id.Provider = strings.ToLower(strings.TrimSpace(id.Provider))
	id.Tenant = strings.TrimSpace(id.Tenant)
	id.Subject = strings.TrimSpace(id.Subject)
	id.Name = strings.TrimSpace(id.Name)
	id.AvatarURL = strings.TrimSpace(id.AvatarURL)
	if id.Provider == "" || id.Subject == "" {
		return id, errors.New("identity provider and subject are required")
	}
	return id, nil
}

// UserByIdentity finds the account bound to (provider, tenant, subject). An
// empty provider or subject never matches.
func (r *UsersRepo) UserByIdentity(provider, tenant, subject string) (domain.User, bool) {
	id, err := ExternalIdentity{Provider: provider, Tenant: tenant, Subject: subject}.normalized()
	if err != nil {
		return domain.User{}, false
	}
	var row userIdentityRow
	res := r.DB.Where("provider = ? AND tenant = ? AND subject = ?", id.Provider, id.Tenant, id.Subject).Limit(1).Find(&row)
	if res.Error != nil || res.RowsAffected == 0 {
		return domain.User{}, false
	}
	return r.User(row.UserID)
}

// IdentitiesOf lists the account's sign-in identities for provider.
func (r *UsersRepo) IdentitiesOf(userID int64, provider string) []ExternalIdentity {
	var rows []userIdentityRow
	if err := r.DB.Where("user_id = ? AND provider = ?", userID, strings.ToLower(strings.TrimSpace(provider))).Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]ExternalIdentity, 0, len(rows))
	for _, row := range rows {
		out = append(out, ExternalIdentity{Provider: row.Provider, Tenant: row.Tenant, Subject: row.Subject, Name: row.DisplayName})
	}
	return out
}

// UserCount is the total number of user rows, across every identity adapter.
// An external sign-in uses it to decide the first-ever account is the admin,
// the same bootstrap rule password registration applies with LocalAccountCount.
func (r *UsersRepo) UserCount() int64 {
	var n int64
	r.DB.Model(&userRow{}).Count(&n)
	return n
}

// CreateIdentityAccount provisions a new account bound to id, with a
// generated login name "<provider>-<12 hex>" that cannot collide with a
// password account. It never reuses an existing user row; when id is already
// bound it returns that account. admin grants the platform admin role (the
// first account of a fresh install, decided by the caller).
func (r *UsersRepo) CreateIdentityAccount(id ExternalIdentity, admin bool) (domain.User, error) {
	id, err := id.normalized()
	if err != nil {
		return domain.User{}, err
	}
	if existing, ok := r.UserByIdentity(id.Provider, id.Tenant, id.Subject); ok {
		return existing, nil
	}
	var lastErr error
	// Retry on the unique indexes so two new members signing in at once
	// never share a login name; a lost identity race resolves to the winner.
	for attempt := 0; attempt < 8; attempt++ {
		login, err := generateIdentityLoginName(id.Provider)
		if err != nil {
			return domain.User{}, err
		}
		if _, taken := r.UserByLoginName(login); taken {
			continue
		}
		var userID int64
		err = r.DB.Transaction(func(tx *gorm.DB) error {
			now := time.Now().UTC()
			user := userRow{LoginName: login, ChineseName: id.Name, Created: &now, Updated: &now}
			if admin {
				user.PlatformRole = domain.PlatformRoleAdmin
			}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
			if r.isPostgres() {
				if err := tx.Exec("SELECT setval(pg_get_serial_sequence('users', 'id'), GREATEST((SELECT MAX(id) FROM users), 1))").Error; err != nil {
					return err
				}
			}
			userID = user.ID
			return tx.Create(&userIdentityRow{
				UserID: user.ID, Provider: id.Provider, Tenant: id.Tenant, Subject: id.Subject,
				DisplayName: id.Name, AvatarURL: nullableIdentityString(id.AvatarURL), Created: &now, LastLogin: &now,
			}).Error
		})
		if err != nil {
			lastErr = err
			if existing, ok := r.UserByIdentity(id.Provider, id.Tenant, id.Subject); ok {
				return existing, nil
			}
			continue
		}
		if saved, ok := r.User(userID); ok {
			return saved, nil
		}
		return domain.User{}, errors.New("could not read provisioned account")
	}
	if lastErr == nil {
		lastErr = errors.New("could not allocate a unique login name")
	}
	return domain.User{}, lastErr
}

// RecordIdentitySignIn refreshes the display name and avatar on a repeat
// sign-in and copies a non-empty name to the account. Roles, credentials and
// the binding itself are left untouched.
func (r *UsersRepo) RecordIdentitySignIn(id ExternalIdentity) error {
	id, err := id.normalized()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row userIdentityRow
		res := tx.Where("provider = ? AND tenant = ? AND subject = ?", id.Provider, id.Tenant, id.Subject).Limit(1).Find(&row)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		updates := map[string]any{"last_login_at": now, "avatar_url": nullableIdentityString(id.AvatarURL)}
		if id.Name != "" {
			updates["display_name"] = id.Name
		}
		if err := tx.Model(&userIdentityRow{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
			return err
		}
		if id.Name == "" {
			return nil
		}
		return tx.Model(&userRow{}).Where("id = ?", row.UserID).Updates(map[string]any{"chinese_name": id.Name, "updated_at": now}).Error
	})
}

func nullableIdentityString(v string) *string {
	if v = strings.TrimSpace(v); v == "" {
		return nil
	}
	return &v
}

// generateIdentityLoginName returns "<provider>-xxxxxxxxxxxx" (12 hex chars),
// which satisfies the registration login pattern and sanitizes for the local
// avatar path.
func generateIdentityLoginName(provider string) (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return provider + "-" + hex.EncodeToString(buf), nil
}
