package store

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"monitor.uz/internal/model"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestEnrollFlow(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	tok, _, err := s.CreateInstallToken(ctx, time.Hour, "srv-01")
	if err != nil {
		t.Fatal(err)
	}
	info := model.Info{Hostname: "srv-01", Mode: "host"}
	id, agentTok, err := s.Enroll(ctx, tok, info)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Enroll(ctx, tok, info); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("install token reused: %v", err)
	}
	got, err := s.AgentByToken(ctx, agentTok)
	if err != nil || got != id {
		t.Fatalf("AgentByToken = %d, %v", got, err)
	}

	// Re-install on the same host keeps the id and rotates the token.
	tok2, _, _ := s.CreateInstallToken(ctx, time.Hour, "")
	id2, agentTok2, err := s.Enroll(ctx, tok2, info)
	if err != nil || id2 != id {
		t.Fatalf("re-enroll: id %d vs %d, %v", id2, id, err)
	}
	if _, err := s.AgentByToken(ctx, agentTok); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("old agent token still valid")
	}
	if ok, _ := s.RevokeAgent(ctx, id); !ok {
		t.Fatal("revoke failed")
	}
	if _, err := s.AgentByToken(ctx, agentTok2); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("revoked token still valid")
	}
}

func TestExpiredInstallToken(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	tok, _, _ := s.CreateInstallToken(ctx, -time.Minute, "")
	if _, _, err := s.Enroll(ctx, tok, model.Info{Hostname: "x", Mode: "host"}); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token accepted: %v", err)
	}
}

func sampleAt(ts int64, cpu float64) model.Sample {
	return model.Sample{TS: ts, CPU: &model.CPU{Usage: cpu, Cores: []float64{cpu}}}
}

func TestRawHistory(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.UnixMilli(1_800_000_000_000)
	var batch []model.Sample
	for i := 0; i < 60; i++ {
		batch = append(batch, sampleAt(now.UnixMilli()-int64(60-i)*1000, float64(i)))
	}
	if err := s.InsertSamples(ctx, 1, batch); err != nil {
		t.Fatal(err)
	}
	h, err := s.History(ctx, 1, []string{"cpu.usage", "nope"}, now.Add(-time.Minute).UnixMilli(), now.UnixMilli(), 600, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(h["cpu.usage"]) != 60 || len(h["nope"]) != 0 {
		t.Fatalf("got %d points", len(h["cpu.usage"]))
	}
	// 6 buckets of 10 s: averages of 0..9, 10..19, ...
	h, _ = s.History(ctx, 1, []string{"cpu.usage"}, now.Add(-time.Minute).UnixMilli(), now.UnixMilli(), 6, now)
	if p := h["cpu.usage"]; len(p) < 6 || len(p) > 7 {
		t.Fatalf("downsample: %v", p)
	}
}

func TestRollupAndRetention(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.UnixMilli(1_800_000_000_000)
	// Two hours of 1 s data, cpu = minute index.
	var batch []model.Sample
	start := now.Add(-2 * time.Hour).UnixMilli()
	for ts := start; ts < now.UnixMilli(); ts += 1000 {
		batch = append(batch, sampleAt(ts, float64((ts-start)/60000)))
	}
	if err := s.InsertSamples(ctx, 1, batch); err != nil {
		t.Fatal(err)
	}
	if err := s.Maintain(ctx, now); err != nil {
		t.Fatal(err)
	}

	// Raw older than retention is gone...
	var n int
	s.db.QueryRow(`SELECT count(*) FROM raw WHERE ts < ?`, now.Add(-s.RawRetention).UnixMilli()).Scan(&n)
	if n != 0 {
		t.Fatalf("%d raw rows survived retention", n)
	}
	// ...but its 10 s averages are still served for long ranges.
	h, err := s.History(ctx, 1, []string{"cpu.usage"}, start, now.UnixMilli(), 5000, now)
	if err != nil {
		t.Fatal(err)
	}
	pts := h["cpu.usage"]
	if len(pts) != 720 { // 2 h / 10 s
		t.Fatalf("got %d rollup points", len(pts))
	}
	if pts[0][1] != 0 || pts[len(pts)-1][1] != 119 {
		t.Fatalf("rollup values: first %v last %v", pts[0], pts[len(pts)-1])
	}
	// Per-core series are raw-only.
	var c int
	s.db.QueryRow(`SELECT count(*) FROM rollup r JOIN series s ON s.id=r.series_id WHERE s.key LIKE 'cpu.core%'`).Scan(&c)
	if c != 0 {
		t.Fatal("per-core series must not be rolled up")
	}

	// Late (buffered) data for an old bucket is merged, not lost.
	late := now.Add(-90*time.Minute).UnixMilli() / 10000 * 10000
	if err := s.InsertSamples(ctx, 1, []model.Sample{sampleAt(late, 1000)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Maintain(ctx, now); err != nil {
		t.Fatal(err)
	}
	h, _ = s.History(ctx, 1, []string{"cpu.usage"}, late, late+5000, 5000, now)
	if len(h["cpu.usage"]) != 1 || h["cpu.usage"][0][1] != 1000 {
		t.Fatalf("late sample not rolled up: %v", h["cpu.usage"])
	}
	// Other slots of that hour kept their values.
	h, _ = s.History(ctx, 1, []string{"cpu.usage"}, late-10000, late-5000, 5000, now)
	if len(h["cpu.usage"]) != 1 || h["cpu.usage"][0][1] == 1000 {
		t.Fatalf("neighbour slot damaged: %v", h["cpu.usage"])
	}

	// History retention drops whole old hours.
	s.HistoryRetention = time.Hour
	s.Maintain(ctx, now)
	h, _ = s.History(ctx, 1, []string{"cpu.usage"}, start, now.Add(-90*time.Minute).UnixMilli(), 5000, now)
	if len(h["cpu.usage"]) != 0 {
		t.Fatalf("history retention: %d points left", len(h["cpu.usage"]))
	}
}

func TestSlotsRoundTrip(t *testing.T) {
	v := decodeSlots(nil)
	if len(v) != 360 || !math.IsNaN(float64(v[0])) {
		t.Fatal("empty blob must decode to NaN slots")
	}
	v[5] = 42.5
	w := decodeSlots(encodeSlots(v))
	if w[5] != 42.5 || !math.IsNaN(float64(w[6])) {
		t.Fatal("round trip failed")
	}
}
