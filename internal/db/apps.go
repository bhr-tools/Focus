package db

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// An app token lets another app (the Assistant…) read the hours timed here.
// The user grants it from the connect page; it never expires and survives a
// password change, until the user disconnects the app. Stored hashed.

// App is a connected app, as listed to the user.
type App struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`        // RFC 3339
	UsedAt    string `json:"used_at,omitempty"` // last read, RFC 3339
}

// CreateAppToken connects an app and returns its token.
func (d *DB) CreateAppToken(name string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	_, err := d.conn.Exec(`INSERT INTO app_tokens (name, token_hash, created_at) VALUES (?, ?, ?)`,
		name, hashToken(token), time.Now().Unix())
	if err != nil {
		return "", err
	}
	return token, nil
}

// UseAppToken reports whether the token belongs to a connected app, and
// records the read.
func (d *DB) UseAppToken(token string) bool {
	if token == "" {
		return false
	}
	res, err := d.conn.Exec(`UPDATE app_tokens SET used_at = ? WHERE token_hash = ?`, time.Now().Unix(), hashToken(token))
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// ListApps returns the connected apps, oldest first.
func (d *DB) ListApps() ([]App, error) {
	rows, err := d.conn.Query(`SELECT id, name, created_at, COALESCE(used_at, 0) FROM app_tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := []App{}
	for rows.Next() {
		var a App
		var created, used int64
		if err := rows.Scan(&a.ID, &a.Name, &created, &used); err != nil {
			return nil, err
		}
		a.CreatedAt = time.Unix(created, 0).Format(time.RFC3339)
		if used > 0 {
			a.UsedAt = time.Unix(used, 0).Format(time.RFC3339)
		}
		apps = append(apps, a)
	}
	return apps, rows.Err()
}

// DeleteApp disconnects an app by id.
func (d *DB) DeleteApp(id int64) error {
	_, err := d.conn.Exec(`DELETE FROM app_tokens WHERE id = ?`, id)
	return err
}

// DeleteAppToken disconnects the app holding the token (the app leaving).
func (d *DB) DeleteAppToken(token string) error {
	_, err := d.conn.Exec(`DELETE FROM app_tokens WHERE token_hash = ?`, hashToken(token))
	return err
}
