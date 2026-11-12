// Package collector reads host metrics from /proc and /sys.
//
// Parsers are pure functions over file contents so they can be tested with
// real samples; Host ties them together and computes rates from counter
// deltas divided by the real elapsed (monotonic) time.
package collector

import (
	"bytes"
	"math"
	"strconv"
)

// counterDelta returns cur-prev for a monotonically increasing kernel
// counter. If the counter went backwards and the previous value fits in 32
// bits, it is treated as a 32-bit wrap. Otherwise the counter was reset
// (device re-created, module reloaded) and ok is false.
func counterDelta(prev, cur uint64) (d uint64, ok bool) {
	if cur >= prev {
		return cur - prev, true
	}
	if prev <= math.MaxUint32 {
		return (math.MaxUint32 - prev) + cur + 1, true
	}
	return 0, false
}

// rate converts a counter delta into a per-second rate.
func rate(prev, cur uint64, seconds float64) (float64, bool) {
	if seconds <= 0 {
		return 0, false
	}
	d, ok := counterDelta(prev, cur)
	if !ok {
		return 0, false
	}
	return float64(d) / seconds, true
}

// pct returns part/total*100, 0 when total is 0, clamped to [0,100].
func pct(part, total float64) float64 {
	if total <= 0 {
		return 0
	}
	v := part / total * 100
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// lines splits b into lines without allocating per-line strings.
func lines(b []byte, fn func(line []byte)) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			fn(b)
			return
		}
		fn(b[:i])
		b = b[i+1:]
	}
}

func parseUint(b []byte) (uint64, error) { return strconv.ParseUint(string(b), 10, 64) }
