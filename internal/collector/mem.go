package collector

import (
	"bytes"
	"fmt"

	"monitor.uz/internal/model"
)

// parseMeminfo reads /proc/meminfo. Values in the file are kB.
func parseMeminfo(b []byte) (*model.Mem, error) {
	vals := map[string]uint64{}
	lines(b, func(l []byte) {
		i := bytes.IndexByte(l, ':')
		if i < 0 {
			return
		}
		f := bytes.Fields(l[i+1:])
		if len(f) == 0 {
			return
		}
		n, err := parseUint(f[0])
		if err != nil {
			return
		}
		if len(f) > 1 && string(f[1]) == "kB" {
			n *= 1024
		}
		vals[string(l[:i])] = n
	})
	total, ok := vals["MemTotal"]
	if !ok || total == 0 {
		return nil, fmt.Errorf("meminfo: MemTotal missing")
	}
	avail, ok := vals["MemAvailable"]
	if !ok {
		return nil, fmt.Errorf("meminfo: MemAvailable missing")
	}
	m := &model.Mem{
		Total:     total,
		Available: avail,
		Buffers:   vals["Buffers"],
		Cached:    vals["Cached"],
		SwapTotal: vals["SwapTotal"],
	}
	if avail < total {
		m.Used = total - avail
	}
	if sf := vals["SwapFree"]; sf < m.SwapTotal {
		m.SwapUsed = m.SwapTotal - sf
	}
	m.UsedPct = round2(pct(float64(m.Used), float64(total)))
	m.SwapPct = round2(pct(float64(m.SwapUsed), float64(m.SwapTotal)))
	return m, nil
}

// parseARC reads /proc/spl/kstat/zfs/arcstats ("name type data" rows) and
// returns the current ARC size and its maximum (c_max) in bytes.
func parseARC(b []byte) (size, max uint64, err error) {
	var gotSize, gotMax bool
	lines(b, func(l []byte) {
		f := bytes.Fields(l)
		if len(f) != 3 {
			return
		}
		switch string(f[0]) {
		case "size":
			if n, e := parseUint(f[2]); e == nil {
				size, gotSize = n, true
			}
		case "c_max":
			if n, e := parseUint(f[2]); e == nil {
				max, gotMax = n, true
			}
		}
	})
	if !gotSize || !gotMax {
		return 0, 0, fmt.Errorf("arcstats: size/c_max missing")
	}
	return size, max, nil
}
