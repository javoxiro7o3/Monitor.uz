package collector

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// netCounters are the cumulative counters of one interface in /proc/net/dev.
type netCounters struct {
	RxBytes, RxPackets, RxErrs, RxDrop uint64
	TxBytes, TxPackets, TxErrs, TxDrop uint64
}

// parseNetDev reads /proc/net/dev. The first two lines are headers.
func parseNetDev(b []byte) (map[string]netCounters, error) {
	out := map[string]netCounters{}
	n := 0
	var perr error
	lines(b, func(l []byte) {
		n++
		if n <= 2 || perr != nil {
			return
		}
		i := bytes.IndexByte(l, ':')
		if i < 0 {
			return
		}
		name := string(bytes.TrimSpace(l[:i]))
		f := bytes.Fields(l[i+1:])
		if len(f) < 16 {
			perr = fmt.Errorf("net/dev: short line for %s", name)
			return
		}
		var v [16]uint64
		for j := range v {
			x, err := parseUint(f[j])
			if err != nil {
				perr = fmt.Errorf("net/dev %s: %w", name, err)
				return
			}
			v[j] = x
		}
		out[name] = netCounters{
			RxBytes: v[0], RxPackets: v[1], RxErrs: v[2], RxDrop: v[3],
			TxBytes: v[8], TxPackets: v[9], TxErrs: v[10], TxDrop: v[11],
		}
	})
	return out, perr
}

// Interfaces created per VM/CT by Proxmox (tap, veth, firewall bridges)
// belong to the VM/CT layer (stage 2) and are skipped at host level.
var skipIfacePrefixes = []string{"lo", "tap", "veth", "fwbr", "fwpr", "fwln", "docker", "br-"}

func skipIface(name string) bool {
	for _, p := range skipIfacePrefixes {
		if name == p || (p != "lo" && strings.HasPrefix(name, p)) {
			return true
		}
	}
	return false
}

// ifaceKind classifies an interface by its /sys/class/net entries.
func ifaceKind(sysRoot, name string) string {
	base := filepath.Join(sysRoot, "class/net", name)
	exists := func(p string) bool { _, err := os.Stat(filepath.Join(base, p)); return err == nil }
	switch {
	case exists("bridge"):
		return "bridge"
	case exists("bonding"):
		return "bond"
	case strings.Contains(name, ".") || strings.HasPrefix(name, "vlan"):
		return "vlan"
	case exists("device"):
		return "phys"
	}
	return "other"
}
