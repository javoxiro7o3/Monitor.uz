package collector

import (
	"bytes"
	"fmt"
	"strconv"

	"monitor.uz/internal/model"
)

// parseLoadavg reads /proc/loadavg: "0.52 0.58 0.59 2/1234 5678".
func parseLoadavg(b []byte) (*model.Load, error) {
	f := bytes.Fields(b)
	if len(f) < 4 {
		return nil, fmt.Errorf("loadavg: unexpected %q", b)
	}
	var l model.Load
	var err error
	for i, dst := range []*float64{&l.L1, &l.L5, &l.L15} {
		if *dst, err = strconv.ParseFloat(string(f[i]), 64); err != nil {
			return nil, fmt.Errorf("loadavg: %w", err)
		}
	}
	rt := bytes.SplitN(f[3], []byte("/"), 2)
	if len(rt) == 2 {
		l.Running, _ = strconv.Atoi(string(rt[0]))
		l.Total, _ = strconv.Atoi(string(rt[1]))
	}
	return &l, nil
}

// parseUptime reads /proc/uptime and returns seconds since boot.
func parseUptime(b []byte) (float64, error) {
	f := bytes.Fields(b)
	if len(f) < 1 {
		return 0, fmt.Errorf("uptime: empty")
	}
	return strconv.ParseFloat(string(f[0]), 64)
}
