package collector

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixtures in testdata/ are verbatim copies of a real Linux 6.x /proc
// (taken on the development machine, not on a Proxmox host).
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCounterDelta(t *testing.T) {
	cases := []struct {
		prev, cur, want uint64
		ok              bool
	}{
		{10, 15, 5, true},
		{5, 5, 0, true},
		{math.MaxUint32 - 2, 3, 6, true},  // 32-bit wrap: ...FD,FE,FF,0,1,2,3
		{math.MaxUint64 - 1, 3, 0, false}, // 64-bit counter went back: reset
		{1 << 40, 100, 0, false},          // reset after reboot/device re-create
	}
	for _, c := range cases {
		got, ok := counterDelta(c.prev, c.cur)
		if got != c.want || ok != c.ok {
			t.Errorf("counterDelta(%d,%d) = %d,%v want %d,%v", c.prev, c.cur, got, ok, c.want, c.ok)
		}
	}
}

func TestRateUsesElapsed(t *testing.T) {
	r, ok := rate(1000, 4000, 1.5)
	if !ok || r != 2000 {
		t.Fatalf("rate = %v,%v want 2000", r, ok)
	}
	if _, ok := rate(1, 2, 0); ok {
		t.Fatal("zero elapsed must not produce a rate")
	}
}

func TestParseStatReal(t *testing.T) {
	ps, err := parseStat(fixture(t, "stat"))
	if err != nil {
		t.Fatal(err)
	}
	if ps.All.total() == 0 || len(ps.Cores) == 0 {
		t.Fatalf("unexpected %+v", ps)
	}
	var sum uint64
	for _, c := range ps.Cores {
		sum += c.User
	}
	// The aggregate line is summed separately by the kernel; on a real
	// system it differs from the per-core sum by a few jiffies.
	if d := math.Abs(float64(sum) - float64(ps.All.User)); d > float64(ps.All.User)/100+5 {
		t.Errorf("sum of cores user %d != aggregate %d", sum, ps.All.User)
	}
}

func TestCPUUsage(t *testing.T) {
	a := procStat{All: cpuTimes{User: 100, System: 50, Idle: 800, IOWait: 50}}
	b := procStat{All: cpuTimes{User: 160, System: 70, Idle: 900, IOWait: 70}}
	// delta: user 60, sys 20, idle 100, iowait 20 => total 200, busy 80
	c, ok := cpuUsage(a, b)
	if !ok {
		t.Fatal("no usage")
	}
	if c.Usage != 40 || c.User != 30 || c.System != 10 || c.IOWait != 10 {
		t.Errorf("got %+v", c)
	}
}

func TestParseMeminfoReal(t *testing.T) {
	m, err := parseMeminfo(fixture(t, "meminfo"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Total == 0 || m.Available > m.Total || m.UsedPct <= 0 || m.UsedPct > 100 {
		t.Fatalf("unexpected %+v", m)
	}
	if m.Total%1024 != 0 {
		t.Error("kB values must be converted to bytes")
	}
}

func TestParseMeminfoMissing(t *testing.T) {
	if _, err := parseMeminfo([]byte("MemTotal: 100 kB\n")); err == nil {
		t.Fatal("expected error without MemAvailable")
	}
}

func TestParseARC(t *testing.T) {
	// Format of /proc/spl/kstat/zfs/arcstats (OpenZFS 2.x), trimmed.
	in := "13 1 0x01 123 33456 3290234 9827340\nname                            type data\nhits                            4    1234\nc_max                           4    8589934592\nsize                            4    4294967296\n"
	size, max, err := parseARC([]byte(in))
	if err != nil || size != 4294967296 || max != 8589934592 {
		t.Fatalf("got %d %d %v", size, max, err)
	}
}

func TestParseLoadavgReal(t *testing.T) {
	l, err := parseLoadavg(fixture(t, "loadavg"))
	if err != nil {
		t.Fatal(err)
	}
	if l.Total == 0 {
		t.Fatalf("unexpected %+v", l)
	}
}

func TestParseUptimeReal(t *testing.T) {
	u, err := parseUptime(fixture(t, "uptime"))
	if err != nil || u <= 0 {
		t.Fatalf("got %v %v", u, err)
	}
}

func TestParseNetDevReal(t *testing.T) {
	m, err := parseNetDev(fixture(t, "net_dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["lo"]; !ok {
		t.Fatalf("lo missing: %v", m)
	}
}

func TestParseNetDevFields(t *testing.T) {
	in := "Inter-|   Receive                                                |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
		"  eno1: 1000 10 1 2 0 0 0 0 2000 20 3 4 0 0 0 0\n"
	m, err := parseNetDev([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := netCounters{1000, 10, 1, 2, 2000, 20, 3, 4}
	if m["eno1"] != want {
		t.Fatalf("got %+v", m["eno1"])
	}
}

func TestSkipIface(t *testing.T) {
	for name, skip := range map[string]bool{
		"lo": true, "tap101i0": true, "veth102i0": true, "fwbr101i0": true,
		"eno1": false, "vmbr0": false, "bond0": false, "vmbr0.20": false, "local": false,
	} {
		if skipIface(name) != skip {
			t.Errorf("skipIface(%q) = %v", name, !skip)
		}
	}
}

func TestParseDiskstatsReal(t *testing.T) {
	m, err := parseDiskstats(fixture(t, "diskstats"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) == 0 {
		t.Fatal("no disks")
	}
}

func TestParseDiskstatsFields(t *testing.T) {
	// 17-field format (kernel >= 5.5): reads merged sectors ms writes merged sectors ms inflight io_ticks weighted discards... flush...
	in := " 8 0 sda 100 5 2000 300 50 6 800 120 0 400 420 0 0 0 0 0 0\n"
	m, err := parseDiskstats([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := diskCounters{Reads: 100, ReadSectors: 2000, ReadMs: 300, Writes: 50, WriteSectors: 800, WriteMs: 120, IOTicks: 400}
	if m["sda"] != want {
		t.Fatalf("got %+v", m["sda"])
	}
}

func TestParsePSIReal(t *testing.T) {
	for _, f := range []string{"pressure_cpu", "pressure_memory", "pressure_io"} {
		p, err := parsePSI(fixture(t, f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if !p.HasFull {
			t.Errorf("%s: full line missing", f)
		}
	}
}

func TestPSIPct(t *testing.T) {
	// 250 ms stalled during 1 s => 25 %
	if v := psiPct(1_000_000, 1_250_000, 1); v != 25 {
		t.Fatalf("got %v", v)
	}
	// 0.5 s stalled during 2 s => 25 %
	if v := psiPct(0, 500_000, 2); v != 25 {
		t.Fatalf("got %v", v)
	}
}

func TestParseMountinfoReal(t *testing.T) {
	all := parseMountinfo(fixture(t, "self_mountinfo"))
	if len(all) == 0 {
		t.Fatal("no mounts")
	}
	for _, m := range selectMounts(all) {
		if !localFSTypes[m.FSType] || m.Root != "/" {
			t.Errorf("unexpected mount selected: %+v", m)
		}
	}
}

func TestSelectMountsProxmox(t *testing.T) {
	// Typical Proxmox ZFS layout (hand-written from the mountinfo format,
	// not captured from a real host).
	in := `25 1 0:23 / / rw,relatime shared:1 - zfs rpool/ROOT/pve-1 rw,xattr,posixacl
26 25 0:24 / /rpool rw,relatime shared:2 - zfs rpool rw,xattr,noacl
27 25 0:25 / /rpool/data/subvol-101-disk-0 rw,relatime shared:3 - zfs rpool/data/subvol-101-disk-0 rw,xattr,posixacl
28 25 0:26 / /etc/pve rw,nosuid,nodev,relatime shared:4 - fuse /dev/fuse rw,user_id=0
29 25 0:27 / /mnt/pve/nfs rw,relatime shared:5 - nfs4 10.0.0.9:/backup rw
30 25 8:1 / /boot/efi rw,relatime shared:6 - vfat /dev/sda2 rw
31 25 0:23 /tmp/systemd-private-x/tmp /tmp rw,relatime shared:7 - zfs rpool/ROOT/pve-1 rw
32 25 8:17 / /mnt/my\040disk rw shared:8 - ext4 /dev/sdb1 rw
`
	got := selectMounts(parseMountinfo([]byte(in)))
	var mounts []string
	for _, m := range got {
		mounts = append(mounts, m.Mount)
	}
	want := "/ /rpool /boot/efi /mnt/my disk"
	if strings.Join(mounts, " ") != want {
		t.Fatalf("got %q want %q", strings.Join(mounts, " "), want)
	}
}

// TestHostCollect runs the collector twice against a fake /proc whose
// counters advance by known amounts and checks the computed rates.
func TestHostCollect(t *testing.T) {
	dir := t.TempDir()
	proc := filepath.Join(dir, "proc")
	sys := filepath.Join(dir, "sys")
	for _, d := range []string{"net", "pressure", "self", "sys/class/net/eno1/device", "sys/block/sda/device"} {
		if strings.HasPrefix(d, "sys/") {
			os.MkdirAll(filepath.Join(dir, d), 0o755)
		} else {
			os.MkdirAll(filepath.Join(proc, d), 0o755)
		}
	}
	write := func(rel, s string) {
		if err := os.WriteFile(filepath.Join(proc, rel), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	netHdr := "h1\nh2\n"
	write("meminfo", string(fixture(t, "meminfo")))
	write("loadavg", "1.00 0.50 0.25 2/300 999\n")
	write("uptime", "1000.5 2000.0\n")
	write("self/mountinfo", "")
	snap := func(cpuUser, idle, rx, sectors, ticks, psiTotal uint64) {
		write("stat", "cpu  "+u(cpuUser)+" 0 0 "+u(idle)+" 0 0 0 0 0 0\ncpu0 "+u(cpuUser)+" 0 0 "+u(idle)+" 0 0 0 0 0 0\nbtime 1\n")
		write("net/dev", netHdr+"  eno1: "+u(rx)+" 10 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n tap100i0: 999 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n")
		write("diskstats", " 8 0 sda 10 0 "+u(sectors)+" 0 0 0 0 0 0 "+u(ticks)+" 0\n 8 1 sda1 10 0 5 0 0 0 0 0 0 0 0\n")
		for _, r := range []string{"cpu", "memory", "io"} {
			write("pressure/"+r, "some avg10=0.00 avg60=0.00 avg300=0.00 total="+u(psiTotal)+"\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
		}
	}
	h := &Host{ProcRoot: proc, SysRoot: sys, FSInterval: time.Hour, ClassifyInterval: time.Hour}

	snap(100, 900, 1000, 0, 0, 0)
	t0 := time.Now()
	s1 := h.Collect(t0)
	if s1.CPU != nil || s1.Net != nil || s1.Mem == nil || s1.Load == nil {
		t.Fatalf("first sample: %+v", s1)
	}

	// 2 s later: 50 % CPU, 4000 B rx => 2000 B/s, 2048 sectors => 512 KiB/s,
	// 500 ms busy => 25 % util, 200 ms stall => 10 % PSI.
	snap(200, 1000, 5000, 2048, 500, 200_000)
	s2 := h.Collect(t0.Add(2 * time.Second))
	if s2.CPU == nil || s2.CPU.Usage != 50 || len(s2.CPU.Cores) != 1 || s2.CPU.Cores[0] != 50 {
		t.Fatalf("cpu: %+v", s2.CPU)
	}
	if len(s2.Net) != 1 || s2.Net[0].Name != "eno1" || s2.Net[0].RxBps != 2000 || s2.Net[0].Kind != "phys" {
		t.Fatalf("net: %+v", s2.Net)
	}
	if len(s2.Disks) != 1 || s2.Disks[0].Name != "sda" || s2.Disks[0].ReadBps != 524288 || s2.Disks[0].UtilPct != 25 {
		t.Fatalf("disk: %+v", s2.Disks)
	}
	if s2.PSI == nil || s2.PSI.CPUSome != 10 {
		t.Fatalf("psi: %+v", s2.PSI)
	}
	flat := s2.Flatten()
	if flat["net.rx_bps"] != 2000 || flat["cpu.usage"] != 50 || flat["disk[sda].util"] != 25 {
		t.Fatalf("flatten: %v", flat)
	}

	// A missing file marks only that section unavailable.
	os.Remove(filepath.Join(proc, "loadavg"))
	s3 := h.Collect(t0.Add(3 * time.Second))
	if s3.Load != nil || !contains(s3.Unavailable, "load") || s3.Mem == nil {
		t.Fatalf("missing loadavg: %+v", s3)
	}
}

func TestCollectRealProc(t *testing.T) {
	if _, err := os.Stat("/proc/stat"); err != nil {
		t.Skip("no /proc")
	}
	h := NewHost()
	h.Collect(time.Now())
	time.Sleep(200 * time.Millisecond)
	s := h.Collect(time.Now())
	if s.CPU == nil || s.Mem == nil || s.Load == nil {
		t.Fatalf("real /proc sample incomplete: %+v", s)
	}
}

func u(v uint64) string { return itoa(v) }

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
