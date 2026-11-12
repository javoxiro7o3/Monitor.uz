package collector

import (
	"bytes"
	"fmt"

	"monitor.uz/internal/model"
)

// cpuTimes are the jiffy counters of one "cpu" line in /proc/stat.
type cpuTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

// total excludes guest/guest_nice: the kernel already counts them in user/nice.
func (t cpuTimes) total() uint64 {
	return t.User + t.Nice + t.System + t.Idle + t.IOWait + t.IRQ + t.SoftIRQ + t.Steal
}

// procStat is the part of /proc/stat we use. All[0] is the aggregate line,
// Cores[i] is cpuN.
type procStat struct {
	All   cpuTimes
	Cores []cpuTimes
}

func parseStat(b []byte) (procStat, error) {
	var ps procStat
	found := false
	var perr error
	lines(b, func(l []byte) {
		if perr != nil || !bytes.HasPrefix(l, []byte("cpu")) {
			return
		}
		f := bytes.Fields(l)
		if len(f) < 9 {
			perr = fmt.Errorf("stat: short cpu line %q", l)
			return
		}
		var v [8]uint64
		for i := range v {
			n, err := parseUint(f[i+1])
			if err != nil {
				perr = fmt.Errorf("stat: %q: %w", l, err)
				return
			}
			v[i] = n
		}
		t := cpuTimes{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]}
		if string(f[0]) == "cpu" {
			ps.All = t
			found = true
		} else {
			ps.Cores = append(ps.Cores, t)
		}
	})
	if perr != nil {
		return ps, perr
	}
	if !found {
		return ps, fmt.Errorf("stat: no aggregate cpu line")
	}
	return ps, nil
}

// cpuUsage computes percentages between two snapshots. Jiffy ratios do not
// depend on elapsed time, so no clock is needed here.
func cpuUsage(prev, cur procStat) (*model.CPU, bool) {
	c, ok := cpuPart(prev.All, cur.All)
	if !ok {
		return nil, false
	}
	if len(prev.Cores) == len(cur.Cores) {
		c.Cores = make([]float64, len(cur.Cores))
		for i := range cur.Cores {
			if cc, ok := cpuPart(prev.Cores[i], cur.Cores[i]); ok {
				c.Cores[i] = cc.Usage
			}
		}
	}
	return c, true
}

func cpuPart(a, b cpuTimes) (*model.CPU, bool) {
	tot, ok := counterDelta(a.total(), b.total())
	if !ok || tot == 0 {
		return nil, false
	}
	d := func(x, y uint64) float64 {
		v, ok := counterDelta(x, y)
		if !ok {
			return 0
		}
		return float64(v)
	}
	t := float64(tot)
	idle := d(a.Idle, b.Idle) + d(a.IOWait, b.IOWait)
	return &model.CPU{
		Usage:  round2(pct(t-idle, t)),
		User:   round2(pct(d(a.User, b.User)+d(a.Nice, b.Nice), t)),
		System: round2(pct(d(a.System, b.System), t)),
		IOWait: round2(pct(d(a.IOWait, b.IOWait), t)),
		Steal:  round2(pct(d(a.Steal, b.Steal), t)),
		IRQ:    round2(pct(d(a.IRQ, b.IRQ)+d(a.SoftIRQ, b.SoftIRQ), t)),
	}, true
}
