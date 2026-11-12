package collector

import (
	"bytes"
	"fmt"
)

// psiTotals are the cumulative stall times (microseconds) of one
// /proc/pressure/* file.
type psiTotals struct {
	Some, Full uint64
	HasFull    bool
}

// parsePSI reads a /proc/pressure file:
//
//	some avg10=0.00 avg60=0.00 avg300=0.00 total=12345
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=0
func parsePSI(b []byte) (psiTotals, error) {
	var t psiTotals
	var gotSome bool
	var perr error
	lines(b, func(l []byte) {
		f := bytes.Fields(l)
		if len(f) == 0 || perr != nil {
			return
		}
		var total uint64
		found := false
		for _, kv := range f[1:] {
			if v, ok := bytes.CutPrefix(kv, []byte("total=")); ok {
				n, err := parseUint(v)
				if err != nil {
					perr = fmt.Errorf("psi: %w", err)
					return
				}
				total, found = n, true
			}
		}
		if !found {
			return
		}
		switch string(f[0]) {
		case "some":
			t.Some, gotSome = total, true
		case "full":
			t.Full, t.HasFull = total, true
		}
	})
	if perr != nil {
		return t, perr
	}
	if !gotSome {
		return t, fmt.Errorf("psi: no 'some' line")
	}
	return t, nil
}

// psiPct converts a stall-time delta (µs) over elapsed seconds into percent.
func psiPct(prev, cur uint64, seconds float64) float64 {
	r, ok := rate(prev, cur, seconds)
	if !ok {
		return 0
	}
	return round2(pct(r/1e6, 1))
}
