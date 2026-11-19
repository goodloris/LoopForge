package repo

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// Feishu (飞书) web login is an identity adapter like local accounts: it owns
// only the feishu_* columns on users and never matches an existing account by
// login name, Chinese name or email. A member is recognised by the pair
// (subject, tenant_key), where subject is the union id, or the open id when
// the app has no union id. Auto-provisioned rows get a generated login name
// that cannot collide with a password account.

// FeishuIdentity is the part of a Feishu user_info reply the login uses.
type FeishuIdentity struct {
	Subject   string // union_id, or open_id when union id is absent
	TenantKey string
	Name      string
	AvatarURL string
}

// UserByFeishuIdentity finds the account bound to (subject, tenant_key).
// An empty subject or tenant key never matches, so a reply missing either
// cannot adopt a NULL-column row.
func (r *UsersRepo) UserByFeishuIdentity(subject, tenantKey string) (domain.User, bool) {
	subject = strings.TrimSpace(subject)
	tenantKey = strings.TrimSpace(tenantKey)
	if subject == "" || tenantKey == "" {
		return domain.User{}, false
	}
	return r.userQuery("u.feishu_subject = ? AND u.feishu_tenant_key = ?", subject, tenantKey)
}

// UserCount is the total number of user rows, across every identity adapter.
// A Feishu sign-in uses it to decide the first-ever account is the admin, the
// same bootstrap rule password registration applies with LocalAccountCount.
func (r *UsersRepo) UserCount() int64 {
	var n int64
	r.DB.Model(&userRow{}).Count(&n)
	return n
}

// CreateFeishuAccount provisions a new account for a Feishu member. It binds
// the generated row to (subject, tenant_key), stores the Chinese name and
// avatar, and never reuses an existing user row. admin grants the platform
// admin role (the first account of a fresh install, decided by the caller).
func (r *UsersRepo) CreateFeishuAccount(id FeishuIdentity, admin bool) (domain.User, error) {
	subject := strings.TrimSpace(id.Subject)
	tenantKey := strings.TrimSpace(id.TenantKey)
	if subject == "" || tenantKey == "" {
		return domain.User{}, errors.New("feishu subject and tenant key are required")
	}
	if existing, ok := r.UserByFeishuIdentity(subject, tenantKey); ok {
		return existing, nil
	}
	now := time.Now().UTC()
	row := userRow{
		LoginName:       "",
		ChineseName:     strings.TrimSpace(id.Name),
		FeishuSubject:   &subject,
		FeishuTenantKey: &tenantKey,
		FeishuAvatarURL: nullableFeishuString(id.AvatarURL),
		Created:         &now,
		Updated:         &now,
	}
	if admin {
		row.PlatformRole = domain.PlatformRoleAdmin
	}
	// A generated login name cannot clash with a password account; retry on
	// the unique index until an unused one lands, so concurrent sign-ins of
	// two new members never share a name.
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		login, err := generateFeishuLoginName()
		if err != nil {
			return domain.User{}, err
		}
		if _, taken := r.UserByLoginName(login); taken {
			continue
		}
		row.ID = 0
		row.LoginName = login
		if err := r.DB.Create(&row).Error; err != nil {
			lastErr = err
			continue // unique-index race on login or identity: try a new name
		}
		if r.isPostgres() {
			if err := r.DB.Exec("SELECT setval(pg_get_serial_sequence('users', 'id'), GREATEST((SELECT MAX(id) FROM users), 1))").Error; err != nil {
				return domain.User{}, err
			}
		}
		if saved, ok := r.User(row.ID); ok {
			return saved, nil
		}
		return domain.User{}, errors.New("could not read provisioned feishu account")
	}
	if lastErr == nil {
		lastErr = errors.New("could not allocate a unique login name for the feishu account")
	}
	return domain.User{}, lastErr
}

// RefreshFeishuProfile updates the stored Chinese name and avatar on a repeat
// sign-in. Credentials, roles and the identity binding are left untouched.
func (r *UsersRepo) RefreshFeishuProfile(userID int64, name, avatarURL string) error {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		updates["chinese_name"] = trimmed
	}
	updates["feishu_avatar_url"] = nullableFeishuString(avatarURL)
	return r.DB.Model(&userRow{}).Where("id = ?", userID).Updates(updates).Error
}

func nullableFeishuString(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}

// generateFeishuLoginName returns a random login name of the shape
// "feishu-xxxxxxxxxxxx" (12 hex chars), which satisfies the registration
// login pattern and sanitizes for the local avatar path.
func generateFeishuLoginName() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "feishu-" + hex.EncodeToString(buf), nil
}
