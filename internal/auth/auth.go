// Package auth implements password hashing (Argon2id), TOTP (RFC 6238)
// and login rate limiting for the panel.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP recommendation: m=19 MiB, t=2, p=1; we use
// more memory since only 2-3 admins log in).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
)

// MinPasswordLen is enforced on new passwords.
const MinPasswordLen = 10

// HashPassword returns an encoded Argon2id hash.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// CheckPassword verifies pw against an encoded hash in constant time.
func CheckPassword(encoded, pw string) bool {
	p := strings.Split(encoded, "$")
	if len(p) != 6 || p[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var par uint8
	if _, err := fmt.Sscanf(p[3], "m=%d,t=%d,p=%d", &m, &t, &par); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(p[4])
	want, err2 := base64.RawStdEncoding.DecodeString(p[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, par, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ValidatePassword checks the password policy.
func ValidatePassword(pw string) error {
	if len([]rune(pw)) < MinPasswordLen {
		return fmt.Errorf("parol kamida %d belgidan iborat bo'lishi kerak", MinPasswordLen)
	}
	return nil
}

// ---------- TOTP ----------

// NewTOTPSecret returns a random base32 secret (160 bits).
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

// TOTPURI builds the otpauth:// URI understood by authenticator apps.
func TOTPURI(secret, user string) string {
	label := url.PathEscape("Monitor.uz:" + user)
	q := url.Values{"secret": {secret}, "issuer": {"Monitor.uz"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// totpCode computes the 6-digit code for a 30 s time step (RFC 6238/4226).
func totpCode(secret string, step int64) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", errors.New("bad secret")
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000), nil
}

// CheckTOTP accepts the code for now ±1 step (clock drift up to 30 s).
func CheckTOTP(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	if _, err := strconv.Atoi(code); err != nil {
		return false
	}
	step := now.Unix() / 30
	ok := false
	for d := int64(-1); d <= 1; d++ {
		c, err := totpCode(secret, step+d)
		if err == nil && subtle.ConstantTimeCompare([]byte(c), []byte(code)) == 1 {
			ok = true
		}
	}
	return ok
}

// ---------- rate limiting ----------

// Limiter blocks keys (IP or username) after too many failures within a
// window.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
}

// NewLimiter allows max failures per window per key.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, fails: map[string][]time.Time{}}
}

func (l *Limiter) prune(key string, now time.Time) []time.Time {
	f := l.fails[key]
	i := 0
	for i < len(f) && now.Sub(f[i]) > l.window {
		i++
	}
	f = f[i:]
	if len(f) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = f
	}
	return f
}

// Blocked reports whether key is locked and for how long.
func (l *Limiter) Blocked(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.prune(key, now)
	if len(f) >= l.max {
		return true, l.window - now.Sub(f[0])
	}
	return false, 0
}

// Fail records a failure.
func (l *Limiter) Fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key, now)
	l.fails[key] = append(l.fails[key], now)
}

// Reset clears a key after a successful login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.fails, key)
	l.mu.Unlock()
}
