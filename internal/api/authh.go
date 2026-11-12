package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"monitor.uz/internal/auth"
	"monitor.uz/internal/store"
)

// Session lifetime: absolute 12 h, logged out after 2 h without activity.
const (
	sessionTTL  = 12 * time.Hour
	sessionIdle = 2 * time.Hour
	cookieName  = "mz_session"
)

type ctxKey int

const userKey ctxKey = 1

type cachedSession struct {
	userID    int64
	expires   time.Time
	lastSeen  time.Time
	lastWrite time.Time
}

type sessionCache struct {
	mu sync.Mutex
	m  map[string]*cachedSession
}

func newSessionCache() *sessionCache { return &sessionCache{m: map[string]*cachedSession{}} }

func (c *sessionCache) purge(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.m {
		if now.After(v.expires) || now.Sub(v.lastSeen) > sessionIdle {
			delete(c.m, k)
		}
	}
}

func hashTok(t string) []byte {
	h := sha256.Sum256([]byte(t))
	return h[:]
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requireAuth checks the session cookie. State-changing requests must
// also carry X-Requested-With (a custom header cannot be sent cross-site
// without CORS, which the hub never allows), on top of SameSite=Strict.
func (s *Server) requireAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.NoAuth {
			h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, int64(0))))
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-Requested-With") != "monitor" {
			errJSON(w, http.StatusForbidden, "so'rov rad etildi")
			return
		}
		uid, ok := s.sessionUser(r)
		if !ok {
			errJSON(w, http.StatusUnauthorized, "kirish talab qilinadi")
			return
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, uid)))
	})
}

func (s *Server) sessionUser(r *http.Request) (int64, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return 0, false
	}
	key := string(hashTok(c.Value))
	now := time.Now()
	s.sessions.mu.Lock()
	cs, ok := s.sessions.m[key]
	s.sessions.mu.Unlock()
	if !ok {
		ss, err := s.store.SessionByHash(r.Context(), []byte(key))
		if err != nil {
			return 0, false
		}
		cs = &cachedSession{userID: ss.UserID, expires: time.UnixMilli(ss.ExpiresAt), lastSeen: time.UnixMilli(ss.LastSeen), lastWrite: time.UnixMilli(ss.LastSeen)}
		s.sessions.mu.Lock()
		s.sessions.m[key] = cs
		s.sessions.mu.Unlock()
	}
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	if now.After(cs.expires) || now.Sub(cs.lastSeen) > sessionIdle {
		delete(s.sessions.m, key)
		go s.store.DeleteSession(context.Background(), []byte(key))
		return 0, false
	}
	// The live stream keeps a session alive only while the tab is open.
	cs.lastSeen = now
	if now.Sub(cs.lastWrite) > time.Minute {
		cs.lastWrite = now
		go s.store.TouchSession(context.Background(), []byte(key), now.UnixMilli())
	}
	return cs.userID, true
}

func currentUser(r *http.Request) int64 {
	v, _ := r.Context().Value(userKey).(int64)
	return v
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, uid int64) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	if err := s.store.CreateSession(r.Context(), hashTok(tok), uid, sessionTTL, clientIP(r)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
	return nil
}

// --- first admin ---

func (s *Server) setupFile() string { return filepath.Join(s.cfg.DataDir, "setup-code") }

// initSetup creates a one-time setup code while no admin exists. It is
// printed in the log and stored in <data>/setup-code (0600), so only
// someone with access to the hub server can create the first admin.
func (s *Server) initSetup(ctx context.Context) error {
	n, err := s.store.UserCount(ctx)
	if err != nil {
		return err
	}
	if n > 0 || s.cfg.NoAuth {
		os.Remove(s.setupFile())
		return nil
	}
	if b, err := os.ReadFile(s.setupFile()); err == nil && len(strings.TrimSpace(string(b))) >= 12 {
		s.setupCode = strings.TrimSpace(string(b))
	} else {
		raw := make([]byte, 9)
		rand.Read(raw)
		s.setupCode = strings.ToUpper(base64.RawURLEncoding.EncodeToString(raw))
		s.setupCode = strings.NewReplacer("-", "X", "_", "Y").Replace(s.setupCode)
		if err := os.WriteFile(s.setupFile(), []byte(s.setupCode+"\n"), 0o600); err != nil {
			return err
		}
	}
	s.log.Warn("no admin yet: open the panel and create the first admin with the setup code", "setup_code", s.setupCode, "file", s.setupFile())
	return nil
}

func (s *Server) handleAuthState(w http.ResponseWriter, r *http.Request) {
	n, _ := s.store.UserCount(r.Context())
	out := map[string]any{"setup_required": n == 0 && !s.cfg.NoAuth, "version": s.cfg.Version}
	if uid, ok := s.sessionUser(r); ok || s.cfg.NoAuth {
		if s.cfg.NoAuth {
			out["user"] = map[string]any{"id": 0, "username": "test"}
		} else if u, err := s.store.UserByID(r.Context(), uid); err == nil {
			out["user"] = u
		}
	}
	writeJSON(w, http.StatusOK, out)
}

var userNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{3,32}$`)

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req struct{ Code, Username, Password string }
	if err := readJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "noto'g'ri so'rov")
		return
	}
	ip := clientIP(r)
	if b, d := s.ipLimit.Blocked(ip, time.Now()); b {
		errJSON(w, http.StatusTooManyRequests, fmt.Sprintf("juda ko'p urinish, %d daqiqadan keyin qayta urining", int(d.Minutes())+1))
		return
	}
	n, err := s.store.UserCount(r.Context())
	if err != nil || n > 0 || s.setupCode == "" {
		errJSON(w, http.StatusConflict, "admin allaqachon yaratilgan")
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToUpper(strings.TrimSpace(req.Code))), []byte(s.setupCode)) != 1 {
		s.ipLimit.Fail(ip, time.Now())
		errJSON(w, http.StatusForbidden, "o'rnatish kodi noto'g'ri")
		return
	}
	if !userNameRe.MatchString(req.Username) {
		errJSON(w, http.StatusBadRequest, "login 3-32 belgi: lotin harflari, raqamlar, . _ -")
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	h, err := auth.HashPassword(req.Password)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	uid, err := s.store.CreateUser(r.Context(), req.Username, h)
	if err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	s.setupCode = ""
	os.Remove(s.setupFile())
	s.log.Info("first admin created", "user", req.Username, "remote", ip)
	if err := s.startSession(w, r, uid); err != nil {
		errJSON(w, http.StatusInternalServerError, "seans yaratilmadi")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password, Code string }
	if err := readJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "noto'g'ri so'rov")
		return
	}
	now := time.Now()
	ip := clientIP(r)
	uname := strings.ToLower(strings.TrimSpace(req.Username))
	if b, d := s.ipLimit.Blocked(ip, now); b {
		errJSON(w, http.StatusTooManyRequests, fmt.Sprintf("juda ko'p noto'g'ri urinish, %d daqiqadan keyin qayta urining", int(d.Minutes())+1))
		return
	}
	if b, d := s.userLimit.Blocked(uname, now); b {
		errJSON(w, http.StatusTooManyRequests, fmt.Sprintf("hisob vaqtincha bloklangan, %d daqiqadan keyin qayta urining", int(d.Minutes())+1))
		return
	}
	fail := func(msg string) {
		s.ipLimit.Fail(ip, now)
		s.userLimit.Fail(uname, now)
		s.log.Warn("login failed", "user", uname, "remote", ip)
		time.Sleep(500 * time.Millisecond)
		errJSON(w, http.StatusUnauthorized, msg)
	}
	u, err := s.store.UserByName(r.Context(), strings.TrimSpace(req.Username))
	if errors.Is(err, store.ErrNotFound) {
		auth.CheckPassword("$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", req.Password) // equal timing
		fail("login yoki parol noto'g'ri")
		return
	}
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	if !auth.CheckPassword(u.PassHash, req.Password) {
		fail("login yoki parol noto'g'ri")
		return
	}
	if u.TOTPEnabled {
		if strings.TrimSpace(req.Code) == "" {
			writeJSON(w, http.StatusOK, map[string]any{"totp_required": true})
			return
		}
		secret, err := s.cfg.Box.Open(u.TOTPSecret)
		if err != nil || !auth.CheckTOTP(secret, req.Code, now) {
			fail("tasdiqlash kodi noto'g'ri")
			return
		}
	}
	s.ipLimit.Reset(ip)
	s.userLimit.Reset(uname)
	if err := s.startSession(w, r, u.ID); err != nil {
		errJSON(w, http.StatusInternalServerError, "seans yaratilmadi")
		return
	}
	s.store.TouchLogin(r.Context(), u.ID)
	s.log.Info("login", "user", u.Username, "remote", ip)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		key := hashTok(c.Value)
		s.sessions.mu.Lock()
		delete(s.sessions.m, string(key))
		s.sessions.mu.Unlock()
		s.store.DeleteSession(r.Context(), key)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) dropUserSessions(uid int64) {
	s.sessions.mu.Lock()
	for k, v := range s.sessions.m {
		if v.userID == uid {
			delete(s.sessions.m, k)
		}
	}
	s.sessions.mu.Unlock()
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct{ Old, New string }
	if err := readJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "noto'g'ri so'rov")
		return
	}
	u, err := s.store.UserByID(r.Context(), currentUser(r))
	if err != nil {
		errJSON(w, http.StatusUnauthorized, "kirish talab qilinadi")
		return
	}
	if !auth.CheckPassword(u.PassHash, req.Old) {
		errJSON(w, http.StatusForbidden, "joriy parol noto'g'ri")
		return
	}
	if err := auth.ValidatePassword(req.New); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	h, _ := auth.HashPassword(req.New)
	if err := s.store.SetPassword(r.Context(), u.ID, h); err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	s.dropUserSessions(u.ID)
	s.startSession(w, r, u.ID) // keep this browser logged in
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTOTPBegin(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.UserByID(r.Context(), currentUser(r))
	if err != nil {
		errJSON(w, http.StatusUnauthorized, "kirish talab qilinadi")
		return
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	s.pendTOTP.Store(u.ID, secret)
	writeJSON(w, http.StatusOK, map[string]any{"secret": secret, "uri": auth.TOTPURI(secret, u.Username)})
}

func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	var req struct{ Code string }
	readJSON(r, &req)
	uid := currentUser(r)
	v, ok := s.pendTOTP.Load(uid)
	if !ok {
		errJSON(w, http.StatusBadRequest, "avval kalit yarating")
		return
	}
	secret := v.(string)
	if !auth.CheckTOTP(secret, req.Code, time.Now()) {
		errJSON(w, http.StatusBadRequest, "kod noto'g'ri: telefon soati to'g'riligini tekshiring")
		return
	}
	enc, err := s.cfg.Box.Seal(secret)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	if err := s.store.SetTOTP(r.Context(), uid, enc, true); err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	s.pendTOTP.Delete(uid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var req struct{ Password string }
	readJSON(r, &req)
	u, err := s.store.UserByID(r.Context(), currentUser(r))
	if err != nil || !auth.CheckPassword(u.PassHash, req.Password) {
		errJSON(w, http.StatusForbidden, "parol noto'g'ri")
		return
	}
	if err := s.store.SetTOTP(r.Context(), u.ID, "", false); err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- users ---

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.store.Users(r.Context())
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": us, "me": currentUser(r)})
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	if err := readJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "noto'g'ri so'rov")
		return
	}
	if !userNameRe.MatchString(req.Username) {
		errJSON(w, http.StatusBadRequest, "login 3-32 belgi: lotin harflari, raqamlar, . _ -")
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	h, _ := auth.HashPassword(req.Password)
	id, err := s.store.CreateUser(r.Context(), req.Username, h)
	if err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Info("user created", "user", req.Username, "by", currentUser(r))
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if id == currentUser(r) {
		errJSON(w, http.StatusBadRequest, "o'zingizni o'chira olmaysiz")
		return
	}
	if n, _ := s.store.UserCount(r.Context()); n <= 1 {
		errJSON(w, http.StatusBadRequest, "oxirgi adminni o'chirib bo'lmaydi")
		return
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		errJSON(w, http.StatusInternalServerError, "xato")
		return
	}
	s.dropUserSessions(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
