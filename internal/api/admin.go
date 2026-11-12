package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"monitor.uz/internal/alert"
	"monitor.uz/internal/store"
)

// --- agents & tokens ---

func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Note     string `json:"note"`
		Mode     string `json:"mode"`
		TTLHours int    `json:"ttl_hours"`
	}
	readJSON(r, &req)
	if req.TTLHours <= 0 || req.TTLHours > 24*7 {
		req.TTLHours = 24
	}
	if req.Mode != "guest" {
		req.Mode = "host"
	}
	tok, exp, err := s.store.CreateInstallToken(r.Context(), time.Duration(req.TTLHours)*time.Hour, req.Note)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": tok, "expires": exp.UnixMilli(),
		"command": installCommand(s.cfg.Pin, s.cfg.PublicURL, tok, req.Mode),
	})
}

func installCommand(pin, url, tok, mode string) string {
	cmd := fmt.Sprintf("curl -fsS --insecure --pinnedpubkey 'sha256//%s' %s/install.sh | sh -s -- --token %s", pin, url, tok)
	if mode == "guest" {
		cmd += " --mode guest"
	}
	return cmd
}

func (s *Server) handleAgentRevoke(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	found, err := s.store.RevokeAgent(r.Context(), id)
	if err != nil || !found {
		errJSON(w, http.StatusNotFound, "topilmadi")
		return
	}
	s.live.setRevoked(id)
	if v, ok := s.live.get(id); ok {
		s.broker.publish("agent", v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAgentDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteAgent(r.Context(), id); err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	s.live.remove(id)
	s.broker.publish("removed", map[string]any{"id": id})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAgentUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct{ Version string }
	readJSON(r, &req)
	if req.Version != "" {
		vs, _ := s.store.AgentVersions(r.Context())
		found := false
		for _, v := range vs {
			found = found || v.Version == req.Version
		}
		if !found {
			errJSON(w, http.StatusBadRequest, "bunday versiya yuklanmagan")
			return
		}
	}
	if err := s.store.SetDesiredVersion(r.Context(), id, req.Version); err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	if d, err := s.store.DesiredVersions(r.Context()); err == nil {
		s.live.setDesired(d)
	}
	s.log.Info("agent update requested", "agent", id, "version", req.Version, "by", currentUser(r))
	if v, ok := s.live.get(id); ok {
		s.broker.publish("agent", v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- agent versions ---

var versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,31}$`)

func (s *Server) handleVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := s.store.AgentVersions(r.Context())
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	def, _ := s.store.Setting(r.Context(), "default_agent_version")
	writeJSON(w, http.StatusOK, map[string]any{"versions": vs, "default": def})
}

// handleVersionUpload: multipart form with "file", "version", "arch".
func (s *Server) handleVersionUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 200<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		errJSON(w, http.StatusBadRequest, "fayl yuklanmadi (200 MB gacha)")
		return
	}
	version := strings.TrimSpace(r.FormValue("version"))
	arch := r.FormValue("arch")
	if arch == "" {
		arch = "amd64"
	}
	if !versionRe.MatchString(version) || (arch != "amd64" && arch != "arm64") {
		errJSON(w, http.StatusBadRequest, "versiya yoki arxitektura noto'g'ri")
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		errJSON(w, http.StatusBadRequest, "fayl tanlanmagan")
		return
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil || string(head) != "\x7fELF" {
		errJSON(w, http.StatusBadRequest, "bu Linux bajariladigan fayl (ELF) emas")
		return
	}
	f.Seek(0, io.SeekStart)
	v, err := s.storeAgentBinary(r.Context(), version, arch, f)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("agent version uploaded", "version", version, "arch", arch, "sha256", v.SHA256, "by", currentUser(r))
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) storeAgentBinary(ctx context.Context, version, arch string, src io.Reader) (store.AgentVersion, error) {
	dir := filepath.Join(s.cfg.DistDir, "agent", version)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return store.AgentVersion{}, err
	}
	dst := filepath.Join(dir, "monitor-agent-linux-"+arch)
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return store.AgentVersion{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), src)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return store.AgentVersion{}, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return store.AgentVersion{}, err
	}
	v := store.AgentVersion{Version: version, Arch: arch, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n, UploadedAt: time.Now().UnixMilli()}
	return v, s.store.AddAgentVersion(ctx, v)
}

// RegisterBundledAgent imports the agent shipped in the install package
// (dist/monitor-agent-linux-*, dist/VERSION) as a known version.
func (s *Server) RegisterBundledAgent(ctx context.Context) {
	b, err := os.ReadFile(filepath.Join(s.cfg.DistDir, "VERSION"))
	if err != nil {
		return
	}
	version := strings.TrimSpace(string(b))
	if !versionRe.MatchString(version) {
		return
	}
	vs, _ := s.store.AgentVersions(ctx)
	for _, arch := range []string{"amd64", "arm64"} {
		p := filepath.Join(s.cfg.DistDir, "monitor-agent-linux-"+arch)
		known := false
		for _, v := range vs {
			known = known || (v.Version == version && v.Arch == arch)
		}
		if known {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		if _, err := s.storeAgentBinary(ctx, version, arch, f); err != nil {
			s.log.Error("registering bundled agent failed", "err", err)
		}
		f.Close()
	}
	if def, _ := s.store.Setting(ctx, "default_agent_version"); def == "" {
		s.store.SetSetting(ctx, "default_agent_version", version)
	}
}

// handleVersionDefault makes a version the one /install.sh installs.
func (s *Server) handleVersionDefault(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	if !versionRe.MatchString(version) {
		errJSON(w, http.StatusBadRequest, "versiya noto'g'ri")
		return
	}
	var sums []string
	copied := 0
	for _, arch := range []string{"amd64", "arm64"} {
		src := filepath.Join(s.cfg.DistDir, "agent", version, "monitor-agent-linux-"+arch)
		dst := filepath.Join(s.cfg.DistDir, "monitor-agent-linux-"+arch)
		in, err := os.Open(src)
		if err != nil {
			if b, err := os.ReadFile(dst); err == nil { // keep other arch as is
				sums = append(sums, fmt.Sprintf("%x  monitor-agent-linux-%s", sha256.Sum256(b), arch))
			}
			continue
		}
		h := sha256.New()
		out, err := os.OpenFile(dst+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err == nil {
			_, err = io.Copy(io.MultiWriter(out, h), in)
			out.Close()
		}
		in.Close()
		if err == nil {
			err = os.Rename(dst+".tmp", dst)
		}
		if err != nil {
			errJSON(w, http.StatusInternalServerError, "nusxalashda xato")
			return
		}
		copied++
		sums = append(sums, fmt.Sprintf("%x  monitor-agent-linux-%s", h.Sum(nil), arch))
	}
	if copied == 0 {
		errJSON(w, http.StatusNotFound, "versiya fayli topilmadi")
		return
	}
	os.WriteFile(filepath.Join(s.cfg.DistDir, "SHA256SUMS"), []byte(strings.Join(sums, "\n")+"\n"), 0o644)
	os.WriteFile(filepath.Join(s.cfg.DistDir, "VERSION"), []byte(version+"\n"), 0o644)
	s.store.SetSetting(r.Context(), "default_agent_version", version)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- alerts ---

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("state") == "history" {
		since := time.Now().Add(-s.store.HistoryRetention).UnixMilli()
		list, err := s.store.AlertHistory(r.Context(), since, 2000)
		if err != nil {
			errJSON(w, http.StatusInternalServerError, "xato")
			return
		}
		writeJSON(w, http.StatusOK, list)
		return
	}
	writeJSON(w, http.StatusOK, s.engine.Active())
}

func (s *Server) handleAlertSilence(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct{ Minutes int }
	readJSON(r, &req)
	if req.Minutes < 0 || req.Minutes > 7*24*60 {
		errJSON(w, http.StatusBadRequest, "0..10080 daqiqa")
		return
	}
	a, found := s.engine.Silence(r.Context(), id, time.Now().Add(time.Duration(req.Minutes)*time.Minute))
	if !found {
		errJSON(w, http.StatusNotFound, "faol alert topilmadi")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

type webhookView struct {
	Enabled  bool   `json:"enabled"`
	Insecure bool   `json:"insecure"`
	Channel  string `json:"channel"`
	URLSet   bool   `json:"url_set"`
	URLHint  string `json:"url_hint,omitempty"` // scheme://host/… (secret path hidden)
}

type storedWebhook struct {
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"` // encrypted
	Insecure bool   `json:"insecure"`
	Channel  string `json:"channel"`
}

func (s *Server) loadWebhook(ctx context.Context) (alert.WebhookConfig, error) {
	var sw storedWebhook
	if err := s.store.GetJSON(ctx, "webhook", &sw); err != nil {
		return alert.WebhookConfig{}, err
	}
	url := ""
	if sw.URL != "" && s.cfg.Box != nil {
		var err error
		if url, err = s.cfg.Box.Open(sw.URL); err != nil {
			return alert.WebhookConfig{}, err
		}
	}
	return alert.WebhookConfig{Enabled: sw.Enabled, URL: url, Insecure: sw.Insecure, Channel: sw.Channel}, nil
}

func urlHint(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return ""
	}
	rest := u[i+3:]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		return u[:i+3+j] + "/…"
	}
	return u
}

func (s *Server) handleAlertSettings(w http.ResponseWriter, r *http.Request) {
	wc, _ := s.loadWebhook(r.Context())
	s.setMu.RLock()
	rules := s.rules
	s.setMu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"rules":   rules,
		"webhook": webhookView{Enabled: wc.Enabled, Insecure: wc.Insecure, Channel: wc.Channel, URLSet: wc.URL != "", URLHint: urlHint(wc.URL)},
		"status":  s.notifier.Status(),
	})
}

type webhookInput struct {
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"` // empty = keep current
	ClearURL bool   `json:"clear_url"`
	Insecure bool   `json:"insecure"`
	Channel  string `json:"channel"`
}

func (s *Server) mergeWebhook(ctx context.Context, in webhookInput) (alert.WebhookConfig, error) {
	cur, err := s.loadWebhook(ctx)
	if err != nil {
		return cur, err
	}
	cur.Enabled, cur.Insecure, cur.Channel = in.Enabled, in.Insecure, strings.TrimSpace(in.Channel)
	if in.ClearURL {
		cur.URL = ""
	}
	if u := strings.TrimSpace(in.URL); u != "" {
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return cur, fmt.Errorf("manzil http:// yoki https:// bilan boshlanishi kerak")
		}
		cur.URL = u
	}
	return cur, nil
}

func (s *Server) handleAlertSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rules   *alert.Rules  `json:"rules"`
		Webhook *webhookInput `json:"webhook"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "noto'g'ri so'rov")
		return
	}
	ctx := r.Context()
	if req.Rules != nil {
		if err := req.Rules.Validate(); err != nil {
			errJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.store.PutJSON(ctx, "alert_rules", req.Rules); err != nil {
			errJSON(w, http.StatusInternalServerError, "xato")
			return
		}
		s.setMu.Lock()
		s.rules = *req.Rules
		s.setMu.Unlock()
	}
	if req.Webhook != nil {
		wc, err := s.mergeWebhook(ctx, *req.Webhook)
		if err != nil {
			errJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		enc, err := s.cfg.Box.Seal(wc.URL)
		if err != nil {
			errJSON(w, http.StatusInternalServerError, "xato")
			return
		}
		if err := s.store.PutJSON(ctx, "webhook", storedWebhook{Enabled: wc.Enabled, URL: enc, Insecure: wc.Insecure, Channel: wc.Channel}); err != nil {
			errJSON(w, http.StatusInternalServerError, "xato")
			return
		}
		s.notifier.Configure(wc)
	}
	s.log.Info("alert settings changed", "by", currentUser(r))
	s.handleAlertSettings(w, r)
}

func (s *Server) handleWebhookTest(w http.ResponseWriter, r *http.Request) {
	var in webhookInput
	readJSON(r, &in)
	wc, err := s.mergeWebhook(r.Context(), in)
	if err == nil {
		err = s.notifier.Test(r.Context(), wc)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- monitored VMs / containers ---

func (s *Server) handleOverrides(w http.ResponseWriter, r *http.Request) {
	s.setMu.RLock()
	defer s.setMu.RUnlock()
	writeJSON(w, http.StatusOK, s.overrides)
}

// handleOverridePut: {"kind":"vm"|"container","key":"<agent>:<vmid|name>","monitored":true|false|null}
func (s *Server) handleOverridePut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind      string `json:"kind"`
		Key       string `json:"key"`
		Monitored *bool  `json:"monitored"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "noto'g'ri so'rov")
		return
	}
	agent, _, ok := strings.Cut(req.Key, ":")
	if _, err := strconv.ParseInt(agent, 10, 64); !ok || err != nil || len(req.Key) > 200 {
		errJSON(w, http.StatusBadRequest, "kalit noto'g'ri")
		return
	}
	s.setMu.Lock()
	var m *map[string]bool
	switch req.Kind {
	case "vm":
		m = &s.overrides.VMs
	case "container":
		m = &s.overrides.Containers
	default:
		s.setMu.Unlock()
		errJSON(w, http.StatusBadRequest, "tur noto'g'ri")
		return
	}
	// Copy-on-write: the alert engine reads the old map without a lock.
	next := make(map[string]bool, len(*m)+1)
	for k, v := range *m {
		next[k] = v
	}
	if req.Monitored == nil {
		delete(next, req.Key)
	} else {
		next[req.Key] = *req.Monitored
	}
	*m = next
	ov := s.overrides
	err := s.store.PutJSON(r.Context(), "overrides", ov)
	s.setMu.Unlock()
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	s.broker.publish("monitoring", ov)
	writeJSON(w, http.StatusOK, ov)
}
