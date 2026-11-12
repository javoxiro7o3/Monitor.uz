package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("format %q", h)
	}
	if !CheckPassword(h, "correct horse battery") || CheckPassword(h, "wrong") || CheckPassword("garbage", "x") {
		t.Fatal("check failed")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("salt not random")
	}
}

// RFC 6238 Appendix B test vectors (SHA-1, secret "12345678901234567890").
func TestTOTPVectors(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	cases := map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"}
	for ts, want := range cases {
		got, err := totpCode(secret, ts/30)
		if err != nil || got != want {
			t.Errorf("t=%d got %s want %s", ts, got, want)
		}
		if !CheckTOTP(secret, want, time.Unix(ts+25, 0)) {
			t.Errorf("t=%d: drift window rejected", ts)
		}
	}
	if CheckTOTP(secret, "287082", time.Unix(59+120, 0)) {
		t.Error("old code accepted")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if b, _ := l.Blocked("ip", now); b {
			t.Fatal("blocked too early")
		}
		l.Fail("ip", now)
	}
	if b, _ := l.Blocked("ip", now); !b {
		t.Fatal("not blocked")
	}
	if b, _ := l.Blocked("ip", now.Add(61*time.Second)); b {
		t.Fatal("not released")
	}
}

func TestURI(t *testing.T) {
	u := TOTPURI("ABC", "admin")
	if !strings.HasPrefix(u, "otpauth://totp/Monitor.uz:admin?") || !strings.Contains(u, "secret=ABC") {
		t.Fatal(u)
	}
}
