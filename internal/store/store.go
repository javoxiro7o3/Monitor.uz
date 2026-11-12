// Package store keeps hub state and metric history in SQLite.
//
// History layout:
//   - raw:    every value at its original resolution, kept RawRetention
//     (1 hour plus a margin so late, buffered samples are still rolled up).
//   - rollup: 10-second averages, one row per series per hour holding 360
//     float32 slots (NaN = no data), kept HistoryRetention (1 week).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: static binary, no CGO
)

// Store wraps the hub database.
type Store struct {
	db *sql.DB

	RawRetention     time.Duration // raw values
	HistoryRetention time.Duration // 10 s rollups

	mu     sync.Mutex
	series map[int64]map[string]int64 // agent -> key -> series id
	dirty  map[int64][2]int64         // agent -> [min,max] ts (ms) ingested since last rollup
}

const schema = `
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS install_tokens (
	hash       BLOB PRIMARY KEY,
	note       TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	used_at    INTEGER,
	used_by    INTEGER
);
CREATE TABLE IF NOT EXISTS agents (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	hostname   TEXT NOT NULL,
	mode       TEXT NOT NULL,
	token_hash BLOB NOT NULL UNIQUE,
	info       TEXT NOT NULL DEFAULT '{}',
	created_at INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL DEFAULT 0,
	revoked    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS series (
	id       INTEGER PRIMARY KEY,
	agent_id INTEGER NOT NULL,
	key      TEXT NOT NULL,
	UNIQUE (agent_id, key)
);
CREATE TABLE IF NOT EXISTS raw (
	series_id INTEGER NOT NULL,
	ts        INTEGER NOT NULL,
	v         REAL NOT NULL,
	PRIMARY KEY (series_id, ts)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS rollup (
	series_id INTEGER NOT NULL,
	hour      INTEGER NOT NULL,
	data      BLOB NOT NULL,
	PRIMARY KEY (series_id, hour)
) WITHOUT ROWID;
`

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "foreign_keys(ON)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	st := &Store{
		db:               db,
		RawRetention:     75 * time.Minute,
		HistoryRetention: 7 * 24 * time.Hour,
		series:           map[int64]map[string]int64{},
		dirty:            map[int64][2]int64{},
	}
	if err := st.migrate2(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return st, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Setting returns a stored setting or "" if unset.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting stores a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}
