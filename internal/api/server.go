// Package api is the hub's HTTP layer: agent enrollment and ingest, the
// panel's JSON API (behind login), the live SSE stream, alerts, settings,
// the install script and the embedded web UI.
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
	"sync"
	"text/template"
	"time"

	"monitor.uz/deploy"
	"monitor.uz/internal/alert"
	"monitor.uz/internal/auth"
	"monitor.uz/internal/model"
	"monitor.uz/internal/store"
)

// Config configures the HTTP server.
type Config struct {
	Version        string
	PublicURL      string        // https://hub:8443, used in the install script
	Pin            string        // SPKI pin of the hub certificate
	DataDir        string        // hub data dir (setup code file)
	DistDir        string        // agent binaries served under /dl/
	OfflineTimeout time.Duration // no data for this long => offline (30 s)
	Web            fs.FS         // embedded panel
	Box            *store.Box    // secret encryption
	NoAuth         bool          // tests only
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

	engine   *alert.Engine
	notifier *alert.Notifier

	setMu     sync.RWMutex
	rules     alert.Rules
	overrides alert.Overrides

	sessions  *sessionCache
	ipLimit   *auth.Limiter
	userLimit *auth.Limiter
	setupCode string
	pendTOTP  sync.Map // user id -> pending TOTP secret during enrolment
}

// New creates the server and loads state from the store.
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
	s := &Server{
		cfg: cfg, store: st, log: log,
		live:      newLive(agents, cfg.OfflineTimeout),
		broker:    newBroker(),
		install:   tmpl,
		started:   time.Now(),
		sessions:  newSessionCache(),
		ipLimit:   auth.NewLimiter(20, 15*time.Minute),
		userLimit: auth.NewLimiter(5, 15*time.Minute),
		rules:     alert.DefaultRules(),
	}
	if saved, err := st.LoadReports(ctx); err == nil {
		s.live.restoreReports(saved)
	}
	if err := st.GetJSON(ctx, "alert_rules", &s.rules); err != nil {
		log.Warn("alert rules unreadable, using defaults", "err", err)
	}
	if err := st.GetJSON(ctx, "overrides", &s.overrides); err != nil {
		log.Warn("overrides unreadable", "err", err)
	}
	if d, err := st.DesiredVersions(ctx); err == nil {
		s.live.setDesired(d)
	}
	s.notifier = alert.NewNotifier(log)
	if wc, err := s.loadWebhook(ctx); err == nil {
		s.notifier.Configure(wc)
	} else {
		log.Error("webhook settings unreadable", "err", err)
	}
	s.engine, err = alert.NewEngine(ctx, st, s.notifier, log,
		func(e alert.Event) { s.broker.publish("alert", e) }, s.live.name)
	if err != nil {
		return nil, err
	}
	s.live.setHealth(s.engine.Health())
	if err := s.initSetup(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Agent endpoints (authenticated by tokens)
	mux.HandleFunc("POST /api/v1/enroll", s.handleEnroll)
	mux.HandleFunc("POST /api/v1/ingest", s.handleIngest)
	// Installation (public: script and binaries contain no secrets)
	mux.HandleFunc("GET /install.sh", s.handleInstallScript)
	mux.HandleFunc("GET /dl/{name}", s.handleDownload)
	mux.HandleFunc("GET /dl/agent/{version}/{name}", s.handleDownloadVersion)
	// Login
	mux.HandleFunc("GET /api/v1/auth/state", s.handleAuthState)
	mux.HandleFunc("POST /api/v1/auth/setup", s.handleSetup)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)

	// Panel API (session required)
	p := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireAuth(h)) }
	p("GET /api/v1/hub", s.handleHub)
	p("GET /api/v1/agents", s.handleAgents)
	p("GET /api/v1/agents/{id}", s.handleAgent)
	p("GET /api/v1/agents/{id}/history", s.handleHistory)
	p("GET /api/v1/agents/{id}/keys", s.handleKeys)
	p("GET /api/v1/stream", s.handleStream)
	p("POST /api/v1/auth/password", s.handlePassword)
	p("POST /api/v1/auth/totp/begin", s.handleTOTPBegin)
	p("POST /api/v1/auth/totp/enable", s.handleTOTPEnable)
	p("POST /api/v1/auth/totp/disable", s.handleTOTPDisable)
	p("GET /api/v1/users", s.handleUsers)
	p("POST /api/v1/users", s.handleUserCreate)
	p("DELETE /api/v1/users/{id}", s.handleUserDelete)
	p("POST /api/v1/tokens", s.handleTokenCreate)
	p("POST /api/v1/agents/{id}/revoke", s.handleAgentRevoke)
	p("DELETE /api/v1/agents/{id}", s.handleAgentDelete)
	p("POST /api/v1/agents/{id}/update", s.handleAgentUpdate)
	p("GET /api/v1/agent-versions", s.handleVersions)
	p("POST /api/v1/agent-versions", s.handleVersionUpload)
	p("POST /api/v1/agent-versions/{version}/default", s.handleVersionDefault)
	p("GET /api/v1/alerts", s.handleAlerts)
	p("POST /api/v1/alerts/{id}/silence", s.handleAlertSilence)
	p("GET /api/v1/alert-settings", s.handleAlertSettings)
	p("PUT /api/v1/alert-settings", s.handleAlertSettingsPut)
	p("POST /api/v1/alert-settings/test", s.handleWebhookTest)
	p("GET /api/v1/monitoring", s.handleOverrides)
	p("PUT /api/v1/monitoring", s.handleOverridePut)

	// Panel files (public; the app shows the login screen)
	mux.Handle("GET /", s.static())
	return securityHeaders(mux)
}

// Background runs periodic jobs until ctx is cancelled.
func (s *Server) Background(ctx context.Context) {
	go s.notifier.Run(ctx)
	offline := time.NewTicker(2 * time.Second)
	alerts := time.NewTicker(5 * time.Second)
	persist := time.NewTicker(15 * time.Second)
	maintain := time.NewTicker(time.Minute)
	defer offline.Stop()
	defer alerts.Stop()
	defer persist.Stop()
	defer maintain.Stop()
	// First alert evaluation waits one offline timeout after start, so
	// agents have a chance to reconnect before "offline" fires.
	alertsFrom := time.Now().Add(s.cfg.OfflineTimeout + 5*time.Second)
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
		case now := <-alerts.C:
			if now.Before(alertsFrom) {
				// Before the first evaluation, agents restored from the
				// database count as offline only if they were offline.
				continue
			}
			s.evalAlerts(ctx, now)
		case <-persist.C:
			s.persistSeen(ctx)
		case now := <-maintain.C:
			t0 := time.Now()
			if err := s.store.Maintain(ctx, now); err != nil {
				s.log.Error("maintenance failed", "err", err)
			} else if d := time.Since(t0); d > 5*time.Second {
				s.log.Warn("maintenance slow", "took", d)
			}
			s.store.PurgeAlerts(ctx, now.Add(-s.store.HistoryRetention).UnixMilli())
			s.store.PurgeSessions(ctx, now.UnixMilli(), now.Add(-sessionIdle).UnixMilli())
			s.sessions.purge(now)
		}
	}
}

func (s *Server) evalAlerts(ctx context.Context, now time.Time) {
	s.setMu.RLock()
	rules, ov := s.rules, s.overrides
	s.setMu.RUnlock()
	s.engine.Tick(ctx, now, rules, ov, s.live.alertStates())
	for _, id := range s.live.setHealth(s.engine.Health()) {
		if v, ok := s.live.get(id); ok {
			s.broker.publish("health", map[string]any{"id": id, "health": v.Health})
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
	return map[string]any{"hub": s.hubInfo(), "agents": s.live.list(), "alerts": s.engine.Active()}
}

func (s *Server) hubInfo() map[string]any {
	return map[string]any{
		"version":         s.cfg.Version,
		"started":         s.started.UnixMilli(),
		"now":             time.Now().UnixMilli(),
		"offline_timeout": s.cfg.OfflineTimeout.Milliseconds(),
		"history_ms":      s.store.HistoryRetention.Milliseconds(),
	}
}

// --- helpers ---

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

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		errJSON(w, http.StatusBadRequest, "noto'g'ri ID")
		return 0, false
	}
	return id, true
}

// --- agent endpoints ---

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
	s.log.Info("agent enrolled", "id", id, "hostname", req.Info.Hostname, "mode", req.Info.Mode, "remote", r.RemoteAddr)
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
	ctx := r.Context()
	id, err := s.store.AgentByToken(ctx, tok)
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
	if err := s.store.InsertSamples(ctx, id, req.Samples); err != nil {
		s.log.Error("storing samples failed", "agent", id, "err", err)
		errJSON(w, http.StatusServiceUnavailable, "store error")
		return
	}
	if req.Info != nil {
		if err := s.store.UpdateAgentInfo(ctx, id, *req.Info); err != nil {
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
		s.broker.publish("backfill", map[string]any{"id": id, "from": req.Samples[0].TS, "to": newest.TS})
	}
	if req.Reports != nil {
		s.ingestReports(ctx, id, req.Reports)
	}
	writeJSON(w, http.StatusOK, model.IngestResponse{Update: s.updateOrder(ctx, id, req.Info)})
}

func (s *Server) ingestReports(ctx context.Context, id int64, r *model.Reports) {
	changed := s.live.setReports(id, r)
	for kind, v := range changed {
		var ts int64
		var series map[string]float64
		switch x := v.(type) {
		case *model.VMReport:
			ts, series = x.TS, x.Flatten()
		case *model.DockerReport:
			ts, series = x.TS, x.Flatten()
		case *model.SensorReport:
			ts, series = x.TS, x.Flatten()
		case *model.HostNetReport:
			ts = x.TS
		case *model.StorageReport:
			ts = x.TS
		case *model.ProcReport:
			ts = x.TS
		}
		if kind != "procs" { // processes change every 5 s; not worth a disk write
			if err := s.store.SaveReport(ctx, id, kind, ts, v); err != nil {
				s.log.Error("saving report failed", "kind", kind, "err", err)
			}
		}
		if len(series) > 0 {
			if err := s.store.InsertPoints(ctx, id, ts, series); err != nil {
				s.log.Error("storing report series failed", "kind", kind, "err", err)
			}
		}
		s.broker.publish("report", map[string]any{"id": id, "kind": kind, "data": v})
	}
}

// updateOrder returns an update instruction when the admin chose another
// version for this agent and that binary is available for its arch.
func (s *Server) updateOrder(ctx context.Context, id int64, info *model.Info) *model.UpdateOrder {
	desired, err := s.store.DesiredVersions(ctx)
	if err != nil {
		return nil
	}
	want := desired[id]
	if want == "" {
		return nil
	}
	cur, arch := s.live.version(id)
	if info != nil {
		cur, arch = info.Version, info.Arch
	}
	if cur == want {
		s.store.SetDesiredVersion(ctx, id, "")
		if d, err := s.store.DesiredVersions(ctx); err == nil {
			s.live.setDesired(d)
		}
		s.log.Info("agent reached desired version", "agent", id, "version", want)
		return nil
	}
	if arch == "" {
		arch = "amd64"
	}
	vs, _ := s.store.AgentVersions(ctx)
	for _, v := range vs {
		if v.Version == want && v.Arch == arch {
			return &model.UpdateOrder{Version: want, SHA256: v.SHA256, URL: "/dl/agent/" + want + "/monitor-agent-linux-" + arch}
		}
	}
	return nil
}

// --- panel API ---

func (s *Server) handleHub(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.hubInfo())
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.live.list())
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, ok := s.live.get(id)
	if !ok {
		errJSON(w, http.StatusNotFound, "topilmadi")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleHistory: ?key=cpu.usage&key=mem.used_pct&from=<ms>&to=<ms>&points=600
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
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
	id, ok := pathID(w, r)
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
		"HubURL":    s.cfg.PublicURL,
		"Pin":       s.cfg.Pin,
		"HostUnit":  deploy.AgentUnit,
		"GuestUnit": deploy.GuestUnit,
	})
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buf.Bytes())
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (s *Server) serveDist(w http.ResponseWriter, r *http.Request, parts ...string) {
	for _, p := range parts {
		if !safeName.MatchString(p) {
			http.NotFound(w, r)
			return
		}
	}
	if s.cfg.DistDir == "" {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(append([]string{s.cfg.DistDir}, parts...)...)
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, p)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	s.serveDist(w, r, r.PathValue("name"))
}

func (s *Server) handleDownloadVersion(w http.ResponseWriter, r *http.Request) {
	s.serveDist(w, r, "agent", r.PathValue("version"), r.PathValue("name"))
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
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; font-src 'self'; media-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}
