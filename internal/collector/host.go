package collector

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"monitor.uz/internal/model"
)

// Host collects host-level metrics. It is not safe for concurrent use.
// Errors never stop collection: the failing section is omitted and its
// name is added to Sample.Unavailable.
type Host struct {
	ProcRoot string // "/proc" in production; a fixture dir in tests
	SysRoot  string // "/sys"

	// FSInterval controls how often filesystems are re-scanned and statvfs'd.
	FSInterval time.Duration
	// ClassifyInterval controls how often new interfaces/disks are classified.
	ClassifyInterval time.Duration

	have     bool
	prevMono time.Time // carries Go's monotonic reading
	prevStat procStat
	prevNet  map[string]netCounters
	prevDisk map[string]diskCounters
	prevPSI  map[string]psiTotals

	fsCache []model.FS
	fsAt    time.Time
	fsNA    bool

	kinds     map[string]string
	diskOK    map[string]bool
	classAt   time.Time
	arcAbsent bool
}

// NewHost returns a collector reading the real /proc and /sys.
func NewHost() *Host {
	return &Host{ProcRoot: "/proc", SysRoot: "/sys", FSInterval: 10 * time.Second, ClassifyInterval: time.Minute}
}

func (h *Host) read(rel string) ([]byte, error) { return os.ReadFile(filepath.Join(h.ProcRoot, rel)) }

// Collect takes one sample. now must come from time.Now() so that it
// carries a monotonic clock reading; rates use now.Sub(previous), not the
// nominal tick interval. The first call has no rates (no previous values).
func (h *Host) Collect(now time.Time) model.Sample {
	s := model.Sample{TS: now.UnixMilli()}
	na := func(name string) { s.Unavailable = append(s.Unavailable, name) }

	var elapsed float64
	if h.have {
		elapsed = now.Sub(h.prevMono).Seconds()
	}
	rates := h.have && elapsed > 0
	if now.Sub(h.classAt) > h.ClassifyInterval || h.kinds == nil {
		h.kinds = map[string]string{}
		h.diskOK = map[string]bool{}
		h.classAt = now
	}

	// CPU
	if b, err := h.read("stat"); err != nil {
		na("cpu")
	} else if st, err := parseStat(b); err != nil {
		na("cpu")
	} else {
		if rates {
			if c, ok := cpuUsage(h.prevStat, st); ok {
				s.CPU = c
			}
		}
		h.prevStat = st
	}

	if b, err := h.read("loadavg"); err == nil {
		if l, err := parseLoadavg(b); err == nil {
			s.Load = l
		} else {
			na("load")
		}
	} else {
		na("load")
	}

	if b, err := h.read("uptime"); err == nil {
		if u, err := parseUptime(b); err == nil {
			s.Uptime = u
		}
	}

	if b, err := h.read("meminfo"); err == nil {
		if m, err := parseMeminfo(b); err == nil {
			s.Mem = m
			if !h.arcAbsent {
				if ab, err := h.read("spl/kstat/zfs/arcstats"); err == nil {
					m.ARCSize, m.ARCMax, _ = parseARC(ab)
				} else if os.IsNotExist(err) {
					h.arcAbsent = true // no ZFS on this host; stop trying
				}
			}
		} else {
			na("mem")
		}
	} else {
		na("mem")
	}

	// Network
	if b, err := h.read("net/dev"); err != nil {
		na("net")
	} else if cur, err := parseNetDev(b); err != nil {
		na("net")
	} else {
		if rates {
			s.Net = h.netRates(cur, elapsed)
		}
		h.prevNet = cur
	}

	// Disk I/O
	if b, err := h.read("diskstats"); err != nil {
		na("disk")
	} else if cur, err := parseDiskstats(b); err != nil {
		na("disk")
	} else {
		if rates {
			s.Disks = h.diskRates(cur, elapsed)
		}
		h.prevDisk = cur
	}

	// Pressure (PSI)
	psi := map[string]psiTotals{}
	for _, r := range []string{"cpu", "memory", "io"} {
		if b, err := h.read("pressure/" + r); err == nil {
			if t, err := parsePSI(b); err == nil {
				psi[r] = t
			}
		}
	}
	if len(psi) < 3 {
		na("psi")
	} else if rates && len(h.prevPSI) == 3 {
		p := h.prevPSI
		s.PSI = &model.PSI{
			CPUSome: psiPct(p["cpu"].Some, psi["cpu"].Some, elapsed),
			CPUFull: psiPct(p["cpu"].Full, psi["cpu"].Full, elapsed),
			MemSome: psiPct(p["memory"].Some, psi["memory"].Some, elapsed),
			MemFull: psiPct(p["memory"].Full, psi["memory"].Full, elapsed),
			IOSome:  psiPct(p["io"].Some, psi["io"].Some, elapsed),
			IOFull:  psiPct(p["io"].Full, psi["io"].Full, elapsed),
		}
	}
	h.prevPSI = psi

	// Filesystems (slower cadence, cached between scans)
	if h.fsAt.IsZero() || now.Sub(h.fsAt) >= h.FSInterval {
		h.fsAt = now
		h.fsCache, h.fsNA = h.scanFS()
	}
	s.FS = h.fsCache
	if h.fsNA {
		na("fs")
	}

	h.prevMono = now
	h.have = true
	return s
}

func (h *Host) netRates(cur map[string]netCounters, sec float64) []model.NetIface {
	out := make([]model.NetIface, 0, len(cur))
	for name, c := range cur {
		if skipIface(name) {
			continue
		}
		p, ok := h.prevNet[name]
		if !ok {
			continue
		}
		rx, ok1 := rate(p.RxBytes, c.RxBytes, sec)
		tx, ok2 := rate(p.TxBytes, c.TxBytes, sec)
		if !ok1 || !ok2 {
			continue // counter reset: skip this interval
		}
		rxp, _ := rate(p.RxPackets, c.RxPackets, sec)
		txp, _ := rate(p.TxPackets, c.TxPackets, sec)
		re, _ := rate(p.RxErrs, c.RxErrs, sec)
		te, _ := rate(p.TxErrs, c.TxErrs, sec)
		rd, _ := rate(p.RxDrop, c.RxDrop, sec)
		td, _ := rate(p.TxDrop, c.TxDrop, sec)
		kind, ok := h.kinds[name]
		if !ok {
			kind = ifaceKind(h.SysRoot, name)
			h.kinds[name] = kind
		}
		out = append(out, model.NetIface{
			Name: name, Kind: kind,
			RxBps: round2(rx), TxBps: round2(tx), RxPps: round2(rxp), TxPps: round2(txp),
			Errs: round2(re + te), Drops: round2(rd + td),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (h *Host) diskRates(cur map[string]diskCounters, sec float64) []model.DiskIO {
	var out []model.DiskIO
	for name, c := range cur {
		ok, known := h.diskOK[name]
		if !known {
			ok = wholeDisk(h.SysRoot, name)
			h.diskOK[name] = ok
		}
		if !ok {
			continue
		}
		p, ok := h.prevDisk[name]
		if !ok {
			continue
		}
		rs, ok1 := rate(p.ReadSectors, c.ReadSectors, sec)
		ws, ok2 := rate(p.WriteSectors, c.WriteSectors, sec)
		if !ok1 || !ok2 {
			continue
		}
		ri, _ := rate(p.Reads, c.Reads, sec)
		wi, _ := rate(p.Writes, c.Writes, sec)
		ticks, _ := counterDelta(p.IOTicks, c.IOTicks)
		dios, _ := counterDelta(p.Reads+p.Writes, c.Reads+c.Writes)
		dms, _ := counterDelta(p.ReadMs+p.WriteMs, c.ReadMs+c.WriteMs)
		var await float64
		if dios > 0 {
			await = float64(dms) / float64(dios)
		}
		out = append(out, model.DiskIO{
			Name:    name,
			ReadBps: round2(rs * sectorSize), WriteBps: round2(ws * sectorSize),
			ReadIOPS: round2(ri), WriteIOPS: round2(wi),
			UtilPct: round2(pct(float64(ticks), sec*1000)),
			AwaitMs: round2(await),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (h *Host) scanFS() ([]model.FS, bool) {
	b, err := h.read("self/mountinfo")
	if err != nil {
		return nil, true
	}
	var out []model.FS
	failed := false
	for _, m := range selectMounts(parseMountinfo(b)) {
		fs, err := statFS(m)
		if err != nil {
			failed = true
			continue
		}
		out = append(out, fs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out, failed && len(out) == 0
}

// ReadInfo gathers static machine information.
func ReadInfo(procRoot, mode, version string) model.Info {
	inf := model.Info{Mode: mode, Version: version, CPUCores: runtime.NumCPU()}
	inf.Hostname, _ = os.Hostname()
	var u syscall.Utsname
	if syscall.Uname(&u) == nil {
		inf.Kernel = utsString(u.Release[:])
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		lines(b, func(l []byte) {
			if v, ok := bytes.CutPrefix(l, []byte("PRETTY_NAME=")); ok {
				inf.OS = strings.Trim(string(v), `"`)
			}
		})
	}
	if b, err := os.ReadFile(filepath.Join(procRoot, "cpuinfo")); err == nil {
		lines(b, func(l []byte) {
			if inf.CPUModel != "" {
				return
			}
			if v, ok := bytes.CutPrefix(l, []byte("model name")); ok {
				if i := bytes.IndexByte(v, ':'); i >= 0 {
					inf.CPUModel = strings.TrimSpace(string(v[i+1:]))
				}
			}
		})
	}
	if b, err := os.ReadFile(filepath.Join(procRoot, "meminfo")); err == nil {
		if m, err := parseMeminfo(b); err == nil {
			inf.MemTotal = m.Total
		}
	}
	if b, err := os.ReadFile(filepath.Join(procRoot, "stat")); err == nil {
		lines(b, func(l []byte) {
			if v, ok := bytes.CutPrefix(l, []byte("btime ")); ok {
				if n, err := parseUint(bytes.TrimSpace(v)); err == nil {
					inf.BootTime = int64(n)
				}
			}
		})
	}
	return inf
}

func utsString[T int8 | uint8](a []T) string {
	b := make([]byte, 0, len(a))
	for _, c := range a {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}
