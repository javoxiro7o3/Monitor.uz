package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"time"

	"monitor.uz/internal/model"
)

// ErrInvalidToken means the token is unknown, expired, used or revoked.
var ErrInvalidToken = errors.New("invalid token")

// Agent is a registered agent.
type Agent struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Hostname  string     `json:"hostname"`
	Mode      string     `json:"mode"`
	Info      model.Info `json:"info"`
	CreatedAt int64      `json:"created_at"`
	LastSeen  int64      `json:"last_seen"` // unix ms
	Revoked   bool       `json:"revoked"`
}

// Tokens are random and high-entropy, so a plain SHA-256 is enough for
// storage: the database never holds a usable token.
func hashToken(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}

func newToken(nbytes int) (string, error) {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

// CreateInstallToken returns a new one-time install token valid for ttl.
func (s *Store) CreateInstallToken(ctx context.Context, ttl time.Duration, note string) (string, time.Time, error) {
	tok, err := newToken(20)
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	exp := now.Add(ttl)
	_, err = s.db.ExecContext(ctx, `INSERT INTO install_tokens(hash,note,created_at,expires_at) VALUES(?,?,?,?)`,
		hashToken(tok), note, now.Unix(), exp.Unix())
	return tok, exp, err
}

// Enroll consumes a one-time install token and returns the agent's id and
// its new permanent token. Re-installing on the same hostname/mode reuses
// the agent record (history stays continuous) and replaces its token.
func (s *Store) Enroll(ctx context.Context, installToken string, info model.Info) (int64, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback()

	now := time.Now()
	res, err := tx.ExecContext(ctx, `UPDATE install_tokens SET used_at=? WHERE hash=? AND used_at IS NULL AND expires_at>?`,
		now.Unix(), hashToken(installToken), now.Unix())
	if err != nil {
		return 0, "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, "", ErrInvalidToken
	}

	agentTok, err := newToken(32)
	if err != nil {
		return 0, "", err
	}
	infoJSON, _ := json.Marshal(info)
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM agents WHERE hostname=? AND mode=?`, info.Hostname, info.Mode).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		r, err := tx.ExecContext(ctx, `INSERT INTO agents(name,hostname,mode,token_hash,info,created_at) VALUES(?,?,?,?,?,?)`,
			info.Hostname, info.Hostname, info.Mode, hashToken(agentTok), string(infoJSON), now.Unix())
		if err != nil {
			return 0, "", err
		}
		id, _ = r.LastInsertId()
	case err != nil:
		return 0, "", err
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET token_hash=?, info=?, revoked=0 WHERE id=?`,
			hashToken(agentTok), string(infoJSON), id); err != nil {
			return 0, "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE install_tokens SET used_by=? WHERE hash=?`, id, hashToken(installToken)); err != nil {
		return 0, "", err
	}
	return id, agentTok, tx.Commit()
}

// AgentByToken resolves a permanent agent token.
func (s *Store) AgentByToken(ctx context.Context, tok string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM agents WHERE token_hash=? AND revoked=0`, hashToken(tok)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrInvalidToken
	}
	return id, err
}

// Agents lists all agents.
func (s *Store) Agents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,hostname,mode,info,created_at,last_seen,revoked FROM agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		var info string
		if err := rows.Scan(&a.ID, &a.Name, &a.Hostname, &a.Mode, &info, &a.CreatedAt, &a.LastSeen, &a.Revoked); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(info), &a.Info)
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAgentInfo stores the latest info reported by an agent.
func (s *Store) UpdateAgentInfo(ctx context.Context, id int64, info model.Info) error {
	b, _ := json.Marshal(info)
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET info=? WHERE id=?`, string(b), id)
	return err
}

// TouchAgents persists last-seen times (unix ms).
func (s *Store) TouchAgents(ctx context.Context, seen map[int64]int64) error {
	if len(seen) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, ts := range seen {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET last_seen=? WHERE id=? AND last_seen<?`, ts, id, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RevokeAgent disables an agent's token; it can no longer send data.
func (s *Store) RevokeAgent(ctx context.Context, id int64) (bool, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE agents SET revoked=1 WHERE id=?`, id)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n == 1, nil
}
