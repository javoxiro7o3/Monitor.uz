package collector

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"monitor.uz/internal/model"
)

// Proxmox reads VM/CT definitions from /etc/pve and their resource usage
// from cgroup v2 (read-only; qm/pct are never executed).
//
//	QEMU VM:  /etc/pve/qemu-server/<id>.conf, cgroup qemu.slice/<id>.scope
//	LXC CT:   /etc/pve/lxc/<id>.conf,         cgroup lxc/<id>
type Proxmox struct {
	PVEDir    string // /etc/pve
	CgroupDir string // /sys/fs/cgroup
	ProcRoot  string // /proc

	prev     map[string]vmCounters // key type:id
	prevMono time.Time
	startAt  map[string]startCache // uptime cache
}

type vmCounters struct {
	cpuUsec, rbytes, wbytes uint64
	netRx, netTx            uint64
	netOK                   bool
}

type startCache struct {
	cgroupID string // detects VM restarts
	start    float64
}

// NewProxmox returns a collector for the real system paths.
func NewProxmox() *Proxmox {
	return &Proxmox{PVEDir: "/etc/pve", CgroupDir: "/sys/fs/cgroup", ProcRoot: "/proc"}
}

// IsProxmox reports whether this host looks like a Proxmox VE node.
func IsProxmox(pveDir string) bool {
	_, err := os.Stat(filepath.Join(pveDir, "qemu-server"))
	return err == nil
}

var confName = regexp.MustCompile(`^(\d+)\.conf$`)

// Collect returns all VMs and CTs.
func (p *Proxmox) Collect(now time.Time) *model.VMReport {
	r := &model.VMReport{TS: now.UnixMilli()}
	if l, err := os.Readlink(filepath.Join(p.PVEDir, "local")); err == nil {
		r.Node = filepath.Base(l)
	}
	var vms []model.VM
	var firstErr error
	for _, typ := range []string{"qemu", "lxc"} {
		dir := "qemu-server"
		if typ == "lxc" {
			dir = "lxc"
		}
		ents, err := os.ReadDir(filepath.Join(p.PVEDir, dir))
		if err != nil {
			if !os.IsNotExist(err) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, e := range ents {
			m := confName.FindStringSubmatch(e.Name())
			if m == nil {
				continue
			}
			b, err := os.ReadFile(filepath.Join(p.PVEDir, dir, e.Name()))
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			id, _ := strconv.Atoi(m[1])
			vms = append(vms, parseVMConf(typ, id, b))
		}
	}
	if firstErr != nil {
		r.Error = firstErr.Error()
	}

	elapsed := 0.0
	if !p.prevMono.IsZero() {
		elapsed = now.Sub(p.prevMono).Seconds()
	}
	netDev := map[string]netCounters{}
	if b, err := os.ReadFile(filepath.Join(p.ProcRoot, "net/dev")); err == nil {
		netDev, _ = parseNetDev(b)
	}
	sysUptime := 0.0
	if b, err := os.ReadFile(filepath.Join(p.ProcRoot, "uptime")); err == nil {
		sysUptime, _ = parseUptime(b)
	}
	cur := map[string]vmCounters{}
	if p.startAt == nil {
		p.startAt = map[string]startCache{}
	}
	for i := range vms {
		v := &vms[i]
		key := v.Type + ":" + strconv.Itoa(v.VMID)
		cg := p.cgroupPath(v)
		if _, err := os.Stat(cg); err != nil {
			v.Status = "stopped"
			delete(p.startAt, key)
			continue
		}
		v.Status = "running"
		c := vmCounters{}
		if b, err := os.ReadFile(filepath.Join(cg, "cpu.stat")); err == nil {
			c.cpuUsec = parseKV(b)["usage_usec"]
		}
		if b, err := os.ReadFile(filepath.Join(cg, "memory.current")); err == nil {
			v.MemUsed, _ = strconv.ParseUint(string(bytes.TrimSpace(b)), 10, 64)
		}
		if b, err := os.ReadFile(filepath.Join(cg, "io.stat")); err == nil {
			c.rbytes, c.wbytes = parseIOStat(b)
		}
		// Host-side tap/veth: what the host receives is what the guest sent.
		prefix := "tap" + strconv.Itoa(v.VMID) + "i"
		if v.Type == "lxc" {
			prefix = "veth" + strconv.Itoa(v.VMID) + "i"
		}
		for name, n := range netDev {
			if strings.HasPrefix(name, prefix) {
				c.netTx += n.RxBytes
				c.netRx += n.TxBytes
				c.netOK = true
			}
		}
		cur[key] = c
		if pc, ok := p.prev[key]; ok && elapsed > 0 {
			cores := v.Cores
			if cores <= 0 {
				cores = float64(runtime.NumCPU())
			}
			if d, ok := counterDelta(pc.cpuUsec, c.cpuUsec); ok {
				v.CPU = round2(pct(float64(d)/1e6/elapsed, cores))
			}
			v.ReadBps, _ = rate(pc.rbytes, c.rbytes, elapsed)
			v.WriteBps, _ = rate(pc.wbytes, c.wbytes, elapsed)
			if c.netOK && pc.netOK {
				v.NetRxBps, _ = rate(pc.netRx, c.netRx, elapsed)
				v.NetTxBps, _ = rate(pc.netTx, c.netTx, elapsed)
			}
			v.ReadBps, v.WriteBps = round2(v.ReadBps), round2(v.WriteBps)
			v.NetRxBps, v.NetTxBps = round2(v.NetRxBps), round2(v.NetTxBps)
		}
		v.Uptime = p.uptime(key, cg, sysUptime)
	}
	p.prev = cur
	p.prevMono = now
	sort.Slice(vms, func(i, j int) bool { return vms[i].VMID < vms[j].VMID })
	r.VMs = vms
	return r
}

func (p *Proxmox) cgroupPath(v *model.VM) string {
	if v.Type == "lxc" {
		return filepath.Join(p.CgroupDir, "lxc", strconv.Itoa(v.VMID))
	}
	return filepath.Join(p.CgroupDir, "qemu.slice", strconv.Itoa(v.VMID)+".scope")
}

// uptime finds the oldest process in the VM's cgroup tree and derives
// uptime from its start time (/proc/<pid>/stat field 22). Cached per
// cgroup instance so the tree is walked only once per VM start.
func (p *Proxmox) uptime(key, cg string, sysUptime float64) float64 {
	id := cgroupInstance(cg)
	if c, ok := p.startAt[key]; ok && c.cgroupID == id && c.start > 0 {
		return max(0, sysUptime-c.start)
	}
	start := -1.0
	depth := 0
	filepath.WalkDir(cg, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			depth = strings.Count(strings.TrimPrefix(path, cg), "/")
			if depth > 3 {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "cgroup.procs" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lines(b, func(l []byte) {
			pid := string(bytes.TrimSpace(l))
			if pid == "" {
				return
			}
			if st, err := os.ReadFile(filepath.Join(p.ProcRoot, pid, "stat")); err == nil {
				if s, ok := procStartSeconds(st); ok && (start < 0 || s < start) {
					start = s
				}
			}
		})
		return nil
	})
	if start < 0 {
		return 0
	}
	p.startAt[key] = startCache{cgroupID: id, start: start}
	return max(0, sysUptime-start)
}

// cgroupInstance identifies one lifetime of a cgroup directory (its inode
// changes when the VM is restarted).
func cgroupInstance(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(fi.ModTime().UnixNano(), 10) + statIno(fi)
}

// clockTicks is USER_HZ, fixed at 100 on Linux for /proc/<pid>/stat.
const clockTicks = 100

// procStartSeconds returns field 22 (starttime) of /proc/<pid>/stat in
// seconds since boot. The comm field may contain spaces and parentheses,
// so parsing starts after the last ')'.
func procStartSeconds(b []byte) (float64, bool) {
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, false
	}
	f := bytes.Fields(b[i+1:])
	// f[0] is field 3 (state); starttime is field 22 => f[19]
	if len(f) < 20 {
		return 0, false
	}
	v, err := parseUint(f[19])
	if err != nil {
		return 0, false
	}
	return float64(v) / clockTicks, true
}

// parseKV reads "key value" lines (cpu.stat, memory.stat).
func parseKV(b []byte) map[string]uint64 {
	m := map[string]uint64{}
	lines(b, func(l []byte) {
		f := bytes.Fields(l)
		if len(f) == 2 {
			if v, err := parseUint(f[1]); err == nil {
				m[string(f[0])] = v
			}
		}
	})
	return m
}

// parseIOStat sums rbytes/wbytes over all devices of a cgroup v2 io.stat:
//
//	8:0 rbytes=1024 wbytes=2048 rios=1 wios=2 dbytes=0 dios=0
func parseIOStat(b []byte) (r, w uint64) {
	lines(b, func(l []byte) {
		for _, kv := range bytes.Fields(l) {
			if v, ok := bytes.CutPrefix(kv, []byte("rbytes=")); ok {
				n, _ := parseUint(v)
				r += n
			} else if v, ok := bytes.CutPrefix(kv, []byte("wbytes=")); ok {
				n, _ := parseUint(v)
				w += n
			}
		}
	})
	return r, w
}

// parseVMConf parses the main section of a Proxmox VM/CT config. Snapshot
// and [PENDING] sections (after the first "[...]" line) are ignored.
func parseVMConf(typ string, id int, b []byte) model.VM {
	v := model.VM{VMID: id, Type: typ}
	kv := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "[") {
			break
		}
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, val, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.TrimSpace(val)
	}
	v.Name = kv["name"]
	if typ == "lxc" {
		v.Name = kv["hostname"]
	}
	if v.Name == "" {
		v.Name = typ + "-" + strconv.Itoa(id)
	}
	v.OnBoot = kv["onboot"] == "1"
	v.Template = kv["template"] == "1"
	v.OSType = kv["ostype"]
	v.Tags = kv["tags"]

	memMB := 512.0
	if m := kv["memory"]; m != "" {
		// PVE 8 may write "memory: current=4096" (with optional extras).
		m = strings.TrimPrefix(firstOpt(m), "current=")
		if f, err := strconv.ParseFloat(m, 64); err == nil {
			memMB = f
		}
	}
	v.MemMax = uint64(memMB * 1024 * 1024)

	if typ == "qemu" {
		cores, sockets := 1.0, 1.0
		if c, err := strconv.ParseFloat(kv["cores"], 64); err == nil {
			cores = c
		}
		if s, err := strconv.ParseFloat(kv["sockets"], 64); err == nil {
			sockets = s
		}
		v.Cores = cores * sockets
		if vc, err := strconv.ParseFloat(kv["vcpus"], 64); err == nil && vc > 0 {
			v.Cores = vc
		}
	} else {
		if c, err := strconv.ParseFloat(kv["cores"], 64); err == nil {
			v.Cores = c
		}
		if l, err := strconv.ParseFloat(kv["cpulimit"], 64); err == nil && l > 0 && (v.Cores == 0 || l < v.Cores) {
			v.Cores = l
		}
	}

	var netKeys []string
	for k := range kv {
		if strings.HasPrefix(k, "net") {
			if _, err := strconv.Atoi(k[3:]); err == nil {
				netKeys = append(netKeys, k)
			}
		}
	}
	sort.Slice(netKeys, func(i, j int) bool {
		a, _ := strconv.Atoi(netKeys[i][3:])
		b, _ := strconv.Atoi(netKeys[j][3:])
		return a < b
	})
	for _, k := range netKeys {
		v.Nets = append(v.Nets, parseNetConf(typ, k, kv[k]))
	}
	return v
}

func firstOpt(s string) string {
	if i := strings.IndexByte(s, ','); i >= 0 {
		return s[:i]
	}
	return s
}

var qemuNICModels = map[string]bool{"virtio": true, "e1000": true, "e1000e": true, "rtl8139": true, "vmxnet3": true, "i82551": true, "i82557b": true, "i82559er": true, "ne2k_isa": true, "ne2k_pci": true, "pcnet": true}

// parseNetConf parses e.g.
//
//	qemu: virtio=BC:24:11:AA:BB:CC,bridge=vmbr0,firewall=1,tag=20
//	lxc:  name=eth0,bridge=vmbr0,firewall=1,hwaddr=BC:24:11:AA:BB:CC,ip=10.0.0.5/24,tag=20,type=veth
func parseNetConf(typ, id, s string) model.VMNet {
	n := model.VMNet{ID: id}
	for _, part := range strings.Split(s, ",") {
		k, val, _ := strings.Cut(part, "=")
		switch {
		case typ == "qemu" && qemuNICModels[k]:
			n.Model, n.MAC = k, strings.ToUpper(val)
		case k == "model":
			n.Model = val
		case k == "macaddr" || k == "hwaddr":
			n.MAC = strings.ToUpper(val)
		case k == "bridge":
			n.Bridge = val
		case k == "tag":
			n.Tag, _ = strconv.Atoi(val)
		case k == "firewall":
			n.Firewall = val == "1"
		case k == "ip":
			if val != "dhcp" && val != "manual" {
				n.IP = val
			}
		case k == "name":
			n.Iface = val
		}
	}
	return n
}
