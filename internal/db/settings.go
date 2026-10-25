package db

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Setting keys.
const (
	SettingWorkDuration  = "work_duration"
	SettingBreakDuration = "break_duration"
	SettingAlarmEnabled  = "alarm_enabled"
	SettingAlarmSound    = "alarm_sound"
	settingPasswordHash  = "password_hash"
)

// PublicSettings lists the settings readable and writable through the API.
var PublicSettings = []string{SettingWorkDuration, SettingBreakDuration, SettingAlarmEnabled, SettingAlarmSound}

// GetSettings returns the public settings.
func (d *DB) GetSettings() (map[string]string, error) {
	rows, err := d.conn.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if isPublicSetting(k) {
			res[k] = v
		}
	}
	return res, rows.Err()
}

// GetIntSetting returns a positive integer setting, or fallback.
func (d *DB) GetIntSetting(key string, fallback int) int {
	var v string
	if err := d.conn.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return fallback
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return n
	}
	return fallback
}

// SetSetting stores a public setting.
func (d *DB) SetSetting(key, val string) error {
	if !isPublicSetting(key) {
		return fmt.Errorf("unknown setting %q", key)
	}
	return d.setSetting(key, val)
}

func (d *DB) setSetting(key, val string) error {
	_, err := d.conn.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, val)
	return err
}

func isPublicSetting(key string) bool {
	for _, k := range PublicSettings {
		if k == key {
			return true
		}
	}
	return false
}

// ==================== Password ====================

const (
	pbkdf2Prefix     = "pbkdf2-sha256"
	pbkdf2Iterations = 600_000
)

// HashPassword returns "pbkdf2-sha256$<iterations>$<salt hex>$<key hex>".
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%x$%x", pbkdf2Prefix, pbkdf2Iterations, salt, key), nil
}

// CheckPassword verifies a password against a hash produced by HashPassword
// or by the legacy single-round "salt$sha256" scheme.
func CheckPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	switch {
	case len(parts) == 4 && parts[0] == pbkdf2Prefix:
		iterations, err := strconv.Atoi(parts[1])
		if err != nil || iterations <= 0 {
			return false
		}
		salt, err1 := hex.DecodeString(parts[2])
		expected, err2 := hex.DecodeString(parts[3])
		if err1 != nil || err2 != nil {
			return false
		}
		key, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
		return err == nil && subtle.ConstantTimeCompare(key, expected) == 1
	case len(parts) == 2:
		salt, err1 := hex.DecodeString(parts[0])
		expected, err2 := hex.DecodeString(parts[1])
		if err1 != nil || err2 != nil {
			return false
		}
		h := sha256.New()
		h.Write(salt)
		h.Write([]byte(password))
		return subtle.ConstantTimeCompare(h.Sum(nil), expected) == 1
	}
	return false
}

// IsLegacyHash reports whether a stored hash should be upgraded.
func IsLegacyHash(stored string) bool {
	return !strings.HasPrefix(stored, pbkdf2Prefix+"$")
}

func (d *DB) GetPasswordHash() (string, error) {
	var val string
	err := d.conn.QueryRow(`SELECT value FROM settings WHERE key = ?`, settingPasswordHash).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return val, err
}

func (d *DB) SetPasswordHash(hash string) error {
	return d.setSetting(settingPasswordHash, hash)
}

// SetPasswordIfUnset stores the first password atomically; it reports false
// when a password already exists.
func (d *DB) SetPasswordIfUnset(hash string) (bool, error) {
	res, err := d.conn.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)`, settingPasswordHash, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (d *DB) HasPassword() (bool, error) {
	h, err := d.GetPasswordHash()
	return h != "", err
}

// ==================== Login tokens ====================

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateAuthToken generates and stores a new login token valid for ttl.
func (d *DB) CreateAuthToken(ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	now := time.Now()
	if _, err := d.conn.Exec(`DELETE FROM auth_tokens WHERE expires_at <= ?`, now.Unix()); err != nil {
		return "", err
	}
	_, err := d.conn.Exec(`INSERT INTO auth_tokens (token_hash, expires_at) VALUES (?, ?)`,
		hashToken(token), now.Add(ttl).Unix())
	if err != nil {
		return "", err
	}
	return token, nil
}

// ValidateAuthToken reports whether the token exists and has not expired.
func (d *DB) ValidateAuthToken(token string) bool {
	if token == "" {
		return false
	}
	var n int
	err := d.conn.QueryRow(`SELECT COUNT(*) FROM auth_tokens WHERE token_hash = ? AND expires_at > ?`,
		hashToken(token), time.Now().Unix()).Scan(&n)
	return err == nil && n > 0
}

func (d *DB) DeleteAuthToken(token string) error {
	_, err := d.conn.Exec(`DELETE FROM auth_tokens WHERE token_hash = ?`, hashToken(token))
	return err
}

// DeleteAllAuthTokens logs every client out (used after a password change).
func (d *DB) DeleteAllAuthTokens() error {
	_, err := d.conn.Exec(`DELETE FROM auth_tokens`)
	return err
}
