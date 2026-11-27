package db

import (
	"fmt"
	"strconv"
	"strings"
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
