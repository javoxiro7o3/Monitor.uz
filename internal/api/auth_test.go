package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"monitor.uz/internal/auth"
	"monitor.uz/internal/store"
)

type authEnv struct {
	ts   *httptest.Server
	cl   *http.Client
	data string
	srv  *Server
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	data := t.TempDir()
	st, err := store.Open(filepath.Join(data, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _ := store.OpenBox(filepath.Join(data, "secret.key"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(context.Background(), Config{Version: "t", Web: fstest.MapFS{"index.html": {Data: []byte("x")}}, Box: box, DataDir: data, DistDir: t.TempDir()}, st, log)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(srv.Handler())
	t.Cleanup(ts.Close)
	cl := ts.Client()
	cl.Jar, _ = cookiejar.New(nil)
	return &authEnv{ts, cl, data, srv}
}

func (e *authEnv) do(t *testing.T, method, path string, body any, csrf bool) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if csrf {
		req.Header.Set("X-Requested-With", "monitor")
	}
	resp, err := e.cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func TestAuthFlow(t *testing.T) {
	e := newAuthEnv(t)
	if code, _ := e.do(t, "GET", "/api/v1/agents", nil, false); code != 401 {
		t.Fatalf("unauthenticated agents: %d", code)
	}
	_, st := e.do(t, "GET", "/api/v1/auth/state", nil, false)
	if st["setup_required"] != true {
		t.Fatalf("state: %v", st)
	}
	codeB, err := os.ReadFile(filepath.Join(e.data, "setup-code"))
	if err != nil {
		t.Fatal(err)
	}
	setupCode := strings.TrimSpace(string(codeB))
	if c, _ := e.do(t, "POST", "/api/v1/auth/setup", map[string]string{"code": "WRONG", "username": "admin", "password": "long-password-1"}, false); c != 403 {
		t.Fatalf("wrong setup code: %d", c)
	}
	if c, out := e.do(t, "POST", "/api/v1/auth/setup", map[string]string{"code": setupCode, "username": "admin", "password": "short"}, false); c != 400 {
		t.Fatalf("short password accepted: %d %v", c, out)
	}
	if c, out := e.do(t, "POST", "/api/v1/auth/setup", map[string]string{"code": setupCode, "username": "admin", "password": "long-password-1"}, false); c != 200 {
		t.Fatalf("setup: %d %v", c, out)
	}
	if _, err := os.Stat(filepath.Join(e.data, "setup-code")); err == nil {
		t.Fatal("setup code file not removed")
	}
	// Session works; state-changing call without the CSRF header is refused.
	if c, _ := e.do(t, "GET", "/api/v1/agents", nil, false); c != 200 {
		t.Fatalf("after setup: %d", c)
	}
	if c, _ := e.do(t, "POST", "/api/v1/tokens", map[string]string{}, false); c != 403 {
		t.Fatalf("csrf: %d", c)
	}
	if c, out := e.do(t, "POST", "/api/v1/tokens", map[string]string{"note": "x"}, true); c != 200 || !strings.Contains(out["command"].(string), "--pinnedpubkey") {
		t.Fatalf("token: %d %v", c, out)
	}
	// Second setup is refused.
	if c, _ := e.do(t, "POST", "/api/v1/auth/setup", map[string]string{"code": setupCode, "username": "x", "password": "long-password-1"}, false); c != 409 {
		t.Fatalf("second setup: %d", c)
	}

	// Enable TOTP.
	_, begin := e.do(t, "POST", "/api/v1/auth/totp/begin", nil, true)
	secret := begin["secret"].(string)
	code := totpNow(t, secret)
	if c, out := e.do(t, "POST", "/api/v1/auth/totp/enable", map[string]string{"code": code}, true); c != 200 {
		t.Fatalf("totp enable: %d %v", c, out)
	}

	// Logout, then login requires the code.
	e.do(t, "POST", "/api/v1/auth/logout", nil, false)
	if c, _ := e.do(t, "GET", "/api/v1/agents", nil, false); c != 401 {
		t.Fatal("still logged in after logout")
	}
	_, out := e.do(t, "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "long-password-1"}, false)
	if out["totp_required"] != true {
		t.Fatalf("totp not required: %v", out)
	}
	if c, _ := e.do(t, "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "long-password-1", "code": totpNow(t, secret)}, false); c != 200 {
		t.Fatalf("login with code: %d", c)
	}
	if c, _ := e.do(t, "GET", "/api/v1/agents", nil, false); c != 200 {
		t.Fatal("not logged in")
	}
}

func TestLoginLockout(t *testing.T) {
	e := newAuthEnv(t)
	h, _ := auth.HashPassword("long-password-1")
	e.srv.store.CreateUser(context.Background(), "admin", h)
	for i := 0; i < 5; i++ {
		if c, _ := e.do(t, "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "bad"}, false); c != 401 {
			t.Fatalf("attempt %d: %d", i, c)
		}
	}
	if c, _ := e.do(t, "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "long-password-1"}, false); c != 429 {
		t.Fatalf("not locked: %d", c)
	}
}

func totpNow(t *testing.T, secret string) string {
	t.Helper()
	for i := 0; i < 1000000; i++ {
		c := strings.Repeat("0", 6-len(itoa(int64(i)))) + itoa(int64(i))
		if auth.CheckTOTP(secret, c, time.Now()) {
			return c
		}
	}
	t.Fatal("no code")
	return ""
}
