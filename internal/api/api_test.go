package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"monitor.uz/internal/agent"
	"monitor.uz/internal/model"
	"monitor.uz/internal/store"
	"monitor.uz/internal/tlsutil"
)

type env struct {
	srv   *Server
	ts    *httptest.Server
	store *store.Store
	pin   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	web := fstest.MapFS{"index.html": {Data: []byte("<html>ok</html>")}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(context.Background(), Config{Version: "test", OfflineTimeout: 30 * time.Second, Web: web}, st, log)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(srv.Handler())
	t.Cleanup(ts.Close)
	pin := tlsutil.SPKIPin(ts.Certificate())
	srv.cfg.PublicURL, srv.cfg.Pin = ts.URL, pin
	return &env{srv, ts, st, pin}
}

func TestEnrollIngestStream(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	// SSE client
	req, _ := http.NewRequestWithContext(ctx, "GET", e.ts.URL+"/api/v1/stream", nil)
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := make(chan string, 32)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if name, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				events <- name
			}
		}
	}()
	waitEvent(t, events, "snapshot")

	// Enroll with a one-time token over the pinned connection.
	tok, _, _ := e.store.CreateInstallToken(ctx, time.Hour, "")
	c, err := agent.NewClient(e.ts.URL, e.pin, "")
	if err != nil {
		t.Fatal(err)
	}
	info := model.Info{Hostname: "srv-01", Mode: "host", Version: "test"}
	er, err := c.Enroll(ctx, tok, info)
	if err != nil {
		t.Fatal(err)
	}
	waitEvent(t, events, "agent")
	if _, err := c.Enroll(ctx, tok, info); err == nil {
		t.Fatal("install token accepted twice")
	}

	// Ingest with the permanent token.
	ac, _ := agent.NewClient(e.ts.URL, e.pin, er.Token)
	now := time.Now().UnixMilli()
	err = ac.Ingest(ctx, &model.IngestRequest{Info: &info, Samples: []model.Sample{
		{TS: now - 1000, CPU: &model.CPU{Usage: 10}},
		{TS: now, CPU: &model.CPU{Usage: 20}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitEvent(t, events, "sample")

	var agents []AgentView
	getJSON(t, e, "/api/v1/agents", &agents)
	if len(agents) != 1 || agents[0].Status != StatusOnline || agents[0].Latest.CPU.Usage != 20 {
		t.Fatalf("agents: %+v", agents)
	}
	var hist struct {
		Series map[string][][2]float64 `json:"series"`
	}
	getJSON(t, e, "/api/v1/agents/1/history?key=cpu.usage&from="+itoa(now-60000)+"&to="+itoa(now+1000), &hist)
	if len(hist.Series["cpu.usage"]) != 2 {
		t.Fatalf("history: %+v", hist)
	}

	// Wrong token is rejected.
	bad, _ := agent.NewClient(e.ts.URL, e.pin, "nope")
	if err := bad.Ingest(ctx, &model.IngestRequest{}); err != agent.ErrUnauthorized {
		t.Fatalf("bad token: %v", err)
	}

	// No data for longer than the timeout => offline, pushed to panels.
	expired := e.srv.live.expire(time.Now().Add(31 * time.Second))
	if len(expired) != 1 || expired[0].Status != StatusOffline {
		t.Fatalf("expire: %+v", expired)
	}
}

func TestPinMismatchRejected(t *testing.T) {
	e := newEnv(t)
	other := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	c, err := agent.NewClient(e.ts.URL, other, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Enroll(context.Background(), "x", model.Info{Hostname: "h", Mode: "host"}); err == nil ||
		!strings.Contains(err.Error(), "pinned") {
		t.Fatalf("expected pin error, got %v", err)
	}
}

func TestInstallScriptAndHeaders(t *testing.T) {
	e := newEnv(t)
	resp, err := e.ts.Client().Get(e.ts.URL + "/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s := string(body)
	if !strings.Contains(s, "HUB_URL='"+e.ts.URL+"'") || !strings.Contains(s, "PIN='"+e.pin+"'") || !strings.Contains(s, "ExecStart=/usr/local/bin/monitor-agent run") {
		t.Fatalf("install script incomplete:\n%s", s)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("CSP missing: %q", csp)
	}
	// Path traversal is refused.
	r2, _ := e.ts.Client().Get(e.ts.URL + "/dl/..%2Fhub.db")
	if r2.StatusCode != http.StatusNotFound {
		t.Fatalf("download traversal: %d", r2.StatusCode)
	}
}

func waitEvent(t *testing.T, ch <-chan string, name string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case got := <-ch:
			if got == name {
				return
			}
		case <-timeout:
			t.Fatalf("event %q not received", name)
		}
	}
}

func getJSON(t *testing.T, e *env, path string, v any) {
	t.Helper()
	resp, err := e.ts.Client().Get(e.ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s: %d %s", path, resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
