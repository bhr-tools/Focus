// Package store opens an app's SQLite database and runs its numbered
// migrations, with the helpers every store uses (transactions, ids).
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bhr-tools/kit/httpx"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Open opens (creating it and its directory if needed) the database at path
// and applies the migrations not run yet. migrations are SQL scripts applied
// in order; PRAGMA user_version records how many ran. Never edit a released
// migration: append a new one instead.
//
// One connection serializes every statement: for a personal app this costs
// nothing and rules out "database is locked" errors.
func Open(path string, migrations []string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := Migrate(db, migrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}

// Migrate applies the migrations above PRAGMA user_version, each in its own
// transaction with the version bump.
func Migrate(db *sql.DB, migrations []string) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("database version %d is newer than this program (%d migrations)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		err := Tx(db, func(tx *sql.Tx) error {
			if _, err := tx.Exec(migrations[i]); err != nil {
				return err
			}
			_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1))
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	return nil
}

// Tx runs fn inside a transaction, committed only if fn returns nil.
func Tx(db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Querier is implemented by both *sql.DB and *sql.Tx.
type Querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Exists reports whether the query returns a row.
func Exists(q Querier, query string, args ...any) (bool, error) {
	var one int
	err := q.QueryRow(query, args...).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// NewID returns a random UUID.
func NewID() string {
	return uuid.NewString()
}

// ClientID returns id when it is a UUID chosen by a client (offline
// creation), or a new id when id is empty.
func ClientID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return NewID(), nil
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return "", httpx.Invalid("invalid id")
	}
	return u.String(), nil
}
