package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"monitor.uz/internal/model"
	"monitor.uz/internal/tlsutil"
)

// State is persisted in <state-dir>/state.json after enrollment.
type State struct {
	Hub     string `json:"hub"`      // https://host:port
	Pin     string `json:"pin"`      // hub SPKI sha256, base64
	AgentID int64  `json:"agent_id"` //
	Token   string `json:"token"`    // permanent agent token (secret)
	Mode    string `json:"mode"`
}

// ErrUnauthorized means the hub rejected the agent token (revoked/unknown).
var ErrUnauthorized = errors.New("hub rejected agent token")

func statePath(dir string) string { return filepath.Join(dir, "state.json") }

// LoadState reads the enrollment state.
func LoadState(dir string) (*State, error) {
	b, err := os.ReadFile(statePath(dir))
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Hub == "" || s.Pin == "" || s.Token == "" {
		return nil, errors.New("incomplete state file")
	}
	return &s, nil
}

// SaveState writes the state file with 0600 permissions.
func SaveState(dir string, s *State) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := statePath(dir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(dir))
}

// Client talks to the hub over HTTPS with a pinned certificate.
type Client struct {
	hub   string
	token string
	http  *http.Client
}

// NewClient builds a pinned HTTPS client.
func NewClient(hub, pin, token string) (*Client, error) {
	cfg, err := tlsutil.PinnedConfig(pin)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		TLSClientConfig:     cfg,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        2,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		Proxy:               nil, // never route internal traffic through a proxy
	}
	return &Client{hub: strings.TrimRight(hub, "/"), token: token, http: &http.Client{Transport: tr, Timeout: 15 * time.Second}}, nil
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(body); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.hub+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		io.Copy(io.Discard, resp.Body)
		return ErrUnauthorized
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("hub: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// Enroll exchanges a one-time install token for a permanent agent token.
func (c *Client) Enroll(ctx context.Context, installToken string, info model.Info) (*model.EnrollResponse, error) {
	var r model.EnrollResponse
	if err := c.post(ctx, "/api/v1/enroll", model.EnrollRequest{Token: installToken, Info: info}, &r); err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return nil, errors.New("install token is invalid, expired or already used")
		}
		return nil, err
	}
	return &r, nil
}

// Ingest delivers a batch of samples.
func (c *Client) Ingest(ctx context.Context, req *model.IngestRequest) error {
	return c.post(ctx, "/api/v1/ingest", req, nil)
}
