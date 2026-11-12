package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"monitor.uz/internal/model"
)

const schema2 = `
CREATE TABLE IF NOT EXISTS agent_reports (
	agent_id INTEGER NOT NULL,
	kind     TEXT NOT NULL,
	ts       INTEGER NOT NULL,
	data     TEXT NOT NULL,
	PRIMARY KEY (agent_id, kind)
);
CREATE TABLE IF NOT EXISTS alerts (
	id             INTEGER PRIMARY KEY,
	key            TEXT NOT NULL,
	rule           TEXT NOT NULL,
	severity       TEXT NOT NULL,
	agent_id       INTEGER NOT NULL,
	object         TEXT NOT NULL DEFAULT '',
	title          TEXT NOT NULL,
	detail         TEXT NOT NULL DEFAULT '',
	started_at     INTEGER NOT NULL,
	resolved_at    INTEGER,
	notified_at    INTEGER,
	silenced_until INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS alerts_open ON alerts(resolved_at);
CREATE INDEX IF NOT EXISTS alerts_started ON alerts(started_at);
CREATE TABLE IF NOT EXISTS users (
	id           INTEGER PRIMARY KEY,
	username     TEXT NOT NULL UNIQUE,
	pass_hash    TEXT NOT NULL,
	totp_secret  TEXT NOT NULL DEFAULT '',
	totp_enabled INTEGER NOT NULL DEFAULT 0,
	created_at   INTEGER NOT NULL,
	last_login   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions (
	hash       BLOB PRIMARY KEY,
	user_id    INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL,
	ip         TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS agent_versions (
	version     TEXT NOT NULL,
	arch        TEXT NOT NULL,
	sha256      TEXT NOT NULL,
	size        INTEGER NOT NULL,
	uploaded_at INTEGER NOT NULL,
	PRIMARY KEY (version, arch)
);
`

func (s *Store) migrate2() error {
	if _, err := s.db.Exec(schema2); err != nil {
		return err
	}
	// Columns added after 0.1.0.
	for _, stmt := range []string{
		`ALTER TABLE agents ADD COLUMN desired_version TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	return nil
}

// ---------- latest reports ----------

// SaveReport persists the newest report of a kind for an agent.
func (s *Store) SaveReport(ctx context.Context, agentID int64, kind string, ts int64, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_reports(agent_id,kind,ts,data) VALUES(?,?,?,?)
		ON CONFLICT(agent_id,kind) DO UPDATE SET ts=excluded.ts, data=excluded.data WHERE excluded.ts >= agent_reports.ts`,
		agentID, kind, ts, string(b))
	return err
}

// LoadReports returns all persisted reports (agent -> kind -> json).
func (s *Store) LoadReports(ctx context.Context) (map[int64]map[string]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id, kind, data FROM agent_reports`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[string]json.RawMessage{}
	for rows.Next() {
		var id int64
		var kind, data string
		if err := rows.Scan(&id, &kind, &data); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]json.RawMessage{}
		}
		out[id][kind] = json.RawMessage(data)
	}
	return out, rows.Err()
}

// InsertPoints stores arbitrary flattened series (VM, container, sensor
// history) at one timestamp.
func (s *Store) InsertPoints(ctx context.Context, agentID, ts int64, vals map[string]float64) error {
	if len(vals) == 0 {
		return nil
	}
	return s.insertFlat(ctx, agentID, []flatPoint{{ts, vals}})
}

// ---------- settings (JSON) ----------

// GetJSON loads a JSON setting into v; missing keys leave v unchanged.
func (s *Store) GetJSON(ctx context.Context, key string, v any) error {
	raw, err := s.Setting(ctx, key)
	if err != nil || raw == "" {
		return err
	}
	return json.Unmarshal([]byte(raw), v)
}

// PutJSON stores v as a JSON setting.
func (s *Store) PutJSON(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, key, string(b))
}

// ---------- secrets ----------

// Box encrypts secrets (webhook URL, TOTP keys) with AES-256-GCM. The key
// lives in a separate 0600 file next to the database, so a copied
// database alone does not reveal the secrets.
type Box struct{ aead cipher.AEAD }

// OpenBox loads or creates the key file.
func OpenBox(keyFile string) (*Box, error) {
	key, err := os.ReadFile(keyFile)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(keyFile, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s: key must be 32 bytes", keyFile)
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &Box{g}, nil
}

// Seal encrypts plaintext ("" stays "").
func (b *Box) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return "enc1:" + base64.StdEncoding.EncodeToString(ct), nil
}

// Open decrypts a value produced by Seal.
func (b *Box) Open(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	raw, ok := strings.CutPrefix(v, "enc1:")
	if !ok {
		return "", errors.New("not an encrypted value")
	}
	ct, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(ct) < b.aead.NonceSize() {
		return "", errors.New("corrupt encrypted value")
	}
	pt, err := b.aead.Open(nil, ct[:b.aead.NonceSize()], ct[b.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("cannot decrypt (wrong key file?)")
	}
	return string(pt), nil
}

// ---------- alerts ----------

// Alert is one alert occurrence.
type Alert struct {
	ID            int64  `json:"id"`
	Key           string `json:"key"`
	Rule          string `json:"rule"`
	Severity      string `json:"severity"` // warning | critical
	AgentID       int64  `json:"agent_id"`
	Object        string `json:"object"`
	Title         string `json:"title"`
	Detail        string `json:"detail"`
	StartedAt     int64  `json:"started_at"` // unix ms
	ResolvedAt    int64  `json:"resolved_at,omitempty"`
	NotifiedAt    int64  `json:"notified_at,omitempty"`
	SilencedUntil int64  `json:"silenced_until,omitempty"`
}

const alertCols = `id,key,rule,severity,agent_id,object,title,detail,started_at,COALESCE(resolved_at,0),COALESCE(notified_at,0),silenced_until`

func scanAlerts(rows *sql.Rows) ([]Alert, error) {
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.Key, &a.Rule, &a.Severity, &a.AgentID, &a.Object, &a.Title, &a.Detail,
			&a.StartedAt, &a.ResolvedAt, &a.NotifiedAt, &a.SilencedUntil); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if out == nil {
		out = []Alert{}
	}
	return out, rows.Err()
}

// OpenAlerts returns unresolved alerts.
func (s *Store) OpenAlerts(ctx context.Context) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+alertCols+` FROM alerts WHERE resolved_at IS NULL ORDER BY started_at DESC`)
	if err != nil {
		return nil, err
	}
	return scanAlerts(rows)
}

// AlertHistory returns alerts that started after since (ms), newest first.
func (s *Store) AlertHistory(ctx context.Context, since int64, limit int) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+alertCols+` FROM alerts WHERE started_at >= ? OR resolved_at IS NULL ORDER BY started_at DESC LIMIT ?`, since, limit)
	if err != nil {
		return nil, err
	}
	return scanAlerts(rows)
}

// InsertAlert stores a new alert and sets its ID.
func (s *Store) InsertAlert(ctx context.Context, a *Alert) error {
	r, err := s.db.ExecContext(ctx, `INSERT INTO alerts(key,rule,severity,agent_id,object,title,detail,started_at) VALUES(?,?,?,?,?,?,?,?)`,
		a.Key, a.Rule, a.Severity, a.AgentID, a.Object, a.Title, a.Detail, a.StartedAt)
	if err != nil {
		return err
	}
	a.ID, err = r.LastInsertId()
	return err
}

// UpdateAlert saves mutable fields.
func (s *Store) UpdateAlert(ctx context.Context, a *Alert) error {
	var resolved, notified any
	if a.ResolvedAt > 0 {
		resolved = a.ResolvedAt
	}
	if a.NotifiedAt > 0 {
		notified = a.NotifiedAt
	}
	_, err := s.db.ExecContext(ctx, `UPDATE alerts SET severity=?, title=?, detail=?, resolved_at=?, notified_at=?, silenced_until=? WHERE id=?`,
		a.Severity, a.Title, a.Detail, resolved, notified, a.SilencedUntil, a.ID)
	return err
}

// PurgeAlerts deletes resolved alerts older than cutoff (ms).
func (s *Store) PurgeAlerts(ctx context.Context, cutoff int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM alerts WHERE resolved_at IS NOT NULL AND resolved_at < ?`, cutoff)
	return err
}

// ---------- users & sessions ----------

// User is a panel administrator.
type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	PassHash    string `json:"-"`
	TOTPSecret  string `json:"-"` // encrypted
	TOTPEnabled bool   `json:"totp_enabled"`
	CreatedAt   int64  `json:"created_at"`
	LastLogin   int64  `json:"last_login"`
}

// ErrNotFound is returned for missing rows.
var ErrNotFound = errors.New("not found")

const userCols = `id,username,pass_hash,totp_secret,totp_enabled,created_at,last_login`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.PassHash, &u.TOTPSecret, &u.TOTPEnabled, &u.CreatedAt, &u.LastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

// UserCount returns the number of users.
func (s *Store) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// UserByName finds a user.
func (s *Store) UserByName(ctx context.Context, name string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username=?`, name))
}

// UserByID finds a user.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id))
}

// Users lists users.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// CreateUser adds a user.
func (s *Store) CreateUser(ctx context.Context, name, passHash string) (int64, error) {
	r, err := s.db.ExecContext(ctx, `INSERT INTO users(username,pass_hash,created_at) VALUES(?,?,?)`, name, passHash, time.Now().UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, errors.New("bu nomdagi foydalanuvchi bor")
		}
		return 0, err
	}
	return r.LastInsertId()
}

// SetPassword changes a password hash and drops the user's sessions.
func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET pass_hash=? WHERE id=?`, hash, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, id)
	return err
}

// SetTOTP stores the encrypted TOTP secret and enabled flag.
func (s *Store) SetTOTP(ctx context.Context, id int64, encSecret string, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET totp_secret=?, totp_enabled=? WHERE id=?`, encSecret, enabled, id)
	return err
}

// DeleteUser removes a user and their sessions.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id=?`, id)
	return err
}

// TouchLogin records a successful login.
func (s *Store) TouchLogin(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login=? WHERE id=?`, time.Now().UnixMilli(), id)
	return err
}

// Session is a logged-in browser.
type Session struct {
	UserID    int64
	ExpiresAt int64
	LastSeen  int64
}

// CreateSession stores a session by token hash.
func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, userID int64, ttl time.Duration, ip string) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(hash,user_id,created_at,expires_at,last_seen,ip) VALUES(?,?,?,?,?,?)`,
		tokenHash, userID, now.UnixMilli(), now.Add(ttl).UnixMilli(), now.UnixMilli(), ip)
	return err
}

// SessionByHash loads a session.
func (s *Store) SessionByHash(ctx context.Context, h []byte) (*Session, error) {
	var ss Session
	err := s.db.QueryRowContext(ctx, `SELECT user_id,expires_at,last_seen FROM sessions WHERE hash=?`, h).Scan(&ss.UserID, &ss.ExpiresAt, &ss.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &ss, err
}

// TouchSession updates last activity.
func (s *Store) TouchSession(ctx context.Context, h []byte, at int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen=? WHERE hash=?`, at, h)
	return err
}

// DeleteSession logs out.
func (s *Store) DeleteSession(ctx context.Context, h []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE hash=?`, h)
	return err
}

// PurgeSessions deletes expired sessions.
func (s *Store) PurgeSessions(ctx context.Context, now, idleCutoff int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ? OR last_seen < ?`, now, idleCutoff)
	return err
}

// ---------- agent versions ----------

// AgentVersion is an uploaded agent binary.
type AgentVersion struct {
	Version    string `json:"version"`
	Arch       string `json:"arch"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	UploadedAt int64  `json:"uploaded_at"`
}

// AddAgentVersion records an uploaded binary.
func (s *Store) AddAgentVersion(ctx context.Context, v AgentVersion) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_versions(version,arch,sha256,size,uploaded_at) VALUES(?,?,?,?,?)
		ON CONFLICT(version,arch) DO UPDATE SET sha256=excluded.sha256, size=excluded.size, uploaded_at=excluded.uploaded_at`,
		v.Version, v.Arch, v.SHA256, v.Size, v.UploadedAt)
	return err
}

// AgentVersions lists uploaded binaries, newest first.
func (s *Store) AgentVersions(ctx context.Context) ([]AgentVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version,arch,sha256,size,uploaded_at FROM agent_versions ORDER BY uploaded_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentVersion{}
	for rows.Next() {
		var v AgentVersion
		if err := rows.Scan(&v.Version, &v.Arch, &v.SHA256, &v.Size, &v.UploadedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteAgentVersion removes a version record.
func (s *Store) DeleteAgentVersion(ctx context.Context, version string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agent_versions WHERE version=?`, version)
	return err
}

// SetDesiredVersion sets (or clears with "") the version an agent should run.
func (s *Store) SetDesiredVersion(ctx context.Context, agentID int64, version string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET desired_version=? WHERE id=?`, version, agentID)
	return err
}

// DesiredVersions returns agent -> desired version (non-empty only).
func (s *Store) DesiredVersions(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, desired_version FROM agents WHERE desired_version != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}

// DeleteAgent removes an agent with its history, reports and alerts.
func (s *Store) DeleteAgent(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM raw WHERE series_id IN (SELECT id FROM series WHERE agent_id=?)`,
		`DELETE FROM rollup WHERE series_id IN (SELECT id FROM series WHERE agent_id=?)`,
		`DELETE FROM series WHERE agent_id=?`,
		`DELETE FROM agent_reports WHERE agent_id=?`,
		`DELETE FROM alerts WHERE agent_id=?`,
		`DELETE FROM agents WHERE id=?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	delete(s.series, id)
	delete(s.dirty, id)
	return nil
}

// reportKinds maps report kinds to their types (for decoding).
var _ = model.Reports{}
