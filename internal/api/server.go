// Package api is the hub's HTTP layer: agent enrollment and ingest, the
// panel's JSON API, the live SSE stream, the install script and the
// embedded web UI.
package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"monitor.uz/deploy"
	"monitor.uz/internal/model"
	"monitor.uz/internal/store"
)

// Config configures the HTTP server.
type Config struct {
	Version        string
	PublicURL      string        // https://hub:8443, used in the install script
	Pin            string        // SPKI pin of the hub certificate
	DistDir        string        // agent binaries served under /dl/
	OfflineTimeout time.Duration // no data for this long => offline (30 s)
	Web            fs.FS         // embedded panel
}

// Server holds hub runtime state.
type Server struct {
	cfg     Config
	store   *store.Store
	log     *slog.Logger
	live    *Live
	broker  *broker
	install *template.Template
	started time.Time
}

// New creates the server and loads agents from the store.
func New(ctx context.Context, cfg Config, st *store.Store, log *slog.Logger) (*Server, error) {
	agents, err := st.Agents(ctx)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("install").Parse(deploy.InstallAgentTmpl)
	if err != nil {
		return nil, err
	}
	if cfg.OfflineTimeout <= 0 {
		cfg.OfflineTimeout = 30 * time.Second
	}
	return &Server{
		cfg: cfg, store: st, log: log,
		live:    newLive(agents, cfg.OfflineTimeout),
		broker:  newBroker(),
		install: tmpl,
		started: time.Now(),
	}, nil
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Agent endpoints (authenticated by tokens)
	mux.HandleFunc("POST /api/v1/enroll", s.handleEnroll)
	mux.HandleFunc("POST /api/v1/ingest", s.handleIngest)
	// Panel API
	mux.HandleFunc("GET /api/v1/hub", s.handleHub)
	mux.HandleFunc("GET /api/v1/agents", s.handleAgents)
	mux.HandleFunc("GET /api/v1/agents/{id}", s.handleAgent)
	mux.HandleFunc("GET /api/v1/agents/{id}/history", s.handleHistory)
	mux.HandleFunc("GET /api/v1/agents/{id}/keys", s.handleKeys)
	mux.HandleFunc("GET /api/v1/stream", s.handleStream)
	// Installation
	mux.HandleFunc("GET /install.sh", s.handleInstallScript)
	mux.HandleFunc("GET /dl/{name}", s.handleDownload)
	// Panel
	mux.Handle("GET /", s.static())
	return securityHeaders(mux)
}

// Background runs periodic jobs until ctx is cancelled.
func (s *Server) Background(ctx context.Context) {
	offline := time.NewTicker(2 * time.Second)
	persist := time.NewTicker(15 * time.Second)
	maintain := time.NewTicker(time.Minute)
	defer offline.Stop()
	defer persist.Stop()
	defer maintain.Stop()
	for {
		select {
		case <-ctx.Done():
			s.persistSeen(context.Background())
			return
		case now := <-offline.C:
			for _, v := range s.live.expire(now) {
				s.log.Warn("agent offline", "agent", v.Name, "id", v.ID)
				s.broker.publish("status", statusMsg(v))
			}
		case <-persist.C:
			s.persistSeen(ctx)
		case now := <-maintain.C:
			t0 := time.Now()
			if err := s.store.Maintain(ctx, now); err != nil {
				s.log.Error("maintenance failed", "err", err)
			} else if d := time.Since(t0); d > 5*time.Second {
				s.log.Warn("maintenance slow", "took", d)
			}
		}
	}
}

func (s *Server) persistSeen(ctx context.Context) {
	if err := s.store.TouchAgents(ctx, s.live.takeDirty()); err != nil {
		s.log.Error("saving last-seen failed", "err", err)
	}
}

func statusMsg(v AgentView) map[string]any {
	return map[string]any{"id": v.ID, "status": v.Status, "last_seen": v.LastSeen}
}

func (s *Server) snapshot() map[string]any {
	return map[string]any{"hub": s.hubInfo(), "agents": s.live.list()}
}

func (s *Server) hubInfo() map[string]any {
	return map[string]any{
		"version":         s.cfg.Version,
		"started":         s.started.UnixMilli(),
		"now":             time.Now().UnixMilli(),
		"offline_timeout": s.cfg.OfflineTimeout.Milliseconds(),
	}
}

// --- agent endpoints ---

const maxBody = 32 << 20 // decompressed request limit

func readJSON(r *http.Request, v any) error {
	var body io.Reader = http.MaxBytesReader(nil, r.Body, maxBody)
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(body)
		if err != nil {
			return err
		}
		defer zr.Close()
		body = io.LimitReader(zr, maxBody)
	}
	return json.NewDecoder(body).Decode(v)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func errJSON(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var req model.EnrollRequest
	if err := readJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "bad request")
		return
	}
	req.Info.Hostname = strings.TrimSpace(req.Info.Hostname)
	if req.Info.Hostname == "" || len(req.Info.Hostname) > 253 || (req.Info.Mode != "host" && req.Info.Mode != "guest") {
		errJSON(w, http.StatusBadRequest, "hostname and mode required")
		return
	}
	id, tok, err := s.store.Enroll(r.Context(), req.Token, req.Info)
	if errors.Is(err, store.ErrInvalidToken) {
		s.log.Warn("enroll rejected", "remote", r.RemoteAddr, "hostname", req.Info.Hostname)
		time.Sleep(time.Second) // slow down guessing
		errJSON(w, http.StatusUnauthorized, "invalid install token")
		return
	}
	if err != nil {
		s.log.Error("enroll failed", "err", err)
		errJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.log.Info("agent enrolled", "id", id, "hostname", req.Info.Hostname, "remote", r.RemoteAddr)
	if agents, err := s.store.Agents(r.Context()); err == nil {
		for _, a := range agents {
			if a.ID == id {
				s.broker.publish("agent", s.live.upsert(a))
			}
		}
	}
	writeJSON(w, http.StatusOK, model.EnrollResponse{AgentID: id, Token: tok})
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		errJSON(w, http.StatusUnauthorized, "token required")
		return
	}
	id, err := s.store.AgentByToken(r.Context(), tok)
	if errors.Is(err, store.ErrInvalidToken) {
		errJSON(w, http.StatusUnauthorized, "invalid token")
		return
	}
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	var req model.IngestRequest
	if err := readJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "bad request")
		return
	}
	sort.Slice(req.Samples, func(i, j int) bool { return req.Samples[i].TS < req.Samples[j].TS })
	if err := s.store.InsertSamples(r.Context(), id, req.Samples); err != nil {
		s.log.Error("storing samples failed", "agent", id, "err", err)
		errJSON(w, http.StatusServiceUnavailable, "store error")
		return
	}
	if req.Info != nil {
		if err := s.store.UpdateAgentInfo(r.Context(), id, *req.Info); err != nil {
			s.log.Error("storing agent info failed", "agent", id, "err", err)
		}
	}
	var newest *model.Sample
	if n := len(req.Samples); n > 0 {
		newest = &req.Samples[n-1]
	}
	cameOnline, view := s.live.seen(id, time.Now(), newest, req.Info)
	if cameOnline {
		s.log.Info("agent online", "agent", view.Name, "id", id)
		s.broker.publish("status", statusMsg(view))
	}
	if req.Info != nil {
		s.broker.publish("agent", view)
	}
	if newest != nil {
		s.broker.publish("sample", map[string]any{"id": id, "s": newest})
	}
	if len(req.Samples) > 1 {
		// Buffered data arrived: tell panels to refetch history.
		s.broker.publish("backfill", map[string]any{"id": id, "from": req.Samples[0].TS, "to": newest.TS})
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- panel API ---

func (s *Server) handleHub(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.hubInfo())
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.live.list())
}

func agentID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		errJSON(w, http.StatusBadRequest, "bad id")
		return 0, false
	}
	return id, true
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := agentID(w, r)
	if !ok {
		return
	}
	v, ok := s.live.get(id)
	if !ok {
		errJSON(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleHistory: ?key=cpu.usage&key=mem.used_pct&from=<ms>&to=<ms>&points=600
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := agentID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	keys := q["key"]
	if len(keys) == 0 || len(keys) > 64 {
		errJSON(w, http.StatusBadRequest, "1..64 key parameters required")
		return
	}
	now := time.Now()
	to := now.UnixMilli()
	from := now.Add(-5 * time.Minute).UnixMilli()
	if v, err := strconv.ParseInt(q.Get("to"), 10, 64); err == nil {
		to = v
	}
	if v, err := strconv.ParseInt(q.Get("from"), 10, 64); err == nil {
		from = v
	}
	points, _ := strconv.Atoi(q.Get("points"))
	if from >= to {
		errJSON(w, http.StatusBadRequest, "from must be before to")
		return
	}
	h, err := s.store.History(r.Context(), id, keys, from, to, points, now)
	if err != nil {
		s.log.Error("history query failed", "err", err)
		errJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "series": h})
}

func (s *Server) handleKeys(w http.ResponseWriter, r *http.Request) {
	id, ok := agentID(w, r)
	if !ok {
		return
	}
	keys, err := s.store.SeriesKeys(r.Context(), id, r.URL.Query().Get("prefix"))
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if keys == nil {
		keys = []string{}
	}
	writeJSON(w, http.StatusOK, keys)
}

// --- installation ---

func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	err := s.install.Execute(&buf, map[string]string{
		"HubURL": s.cfg.PublicURL,
		"Pin":    s.cfg.Pin,
		"Unit":   deploy.AgentUnit,
	})
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buf.Bytes())
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !safeName.MatchString(name) || s.cfg.DistDir == "" {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(s.cfg.DistDir, name)
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, p)
}

// --- panel static files ---

func (s *Server) static() http.Handler {
	fsrv := http.FileServer(http.FS(s.cfg.Web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		fsrv.ServeHTTP(w, r)
	})
}

// securityHeaders forbids loading anything from outside the hub (the panel
// must work offline) and framing.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}
