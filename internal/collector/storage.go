package collector

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"monitor.uz/internal/model"
)

// Storage collects physical disks and RAID state. Sources, in order:
//   - ssacli (HPE Smart Array): controllers, logical drives, physical disks
//   - smartctl: SMART health, temperature, power-on hours (direct disks,
//     and disks behind Smart Array via -d cciss,N)
//   - /proc/mdstat (Linux software RAID) and `zpool list` (ZFS pools)
//
// All tools need root. Every command has a timeout; a failing tool only
// adds an error string to the report.
type Storage struct {
	Run      Runner
	SysRoot  string
	ProcRoot string
	SSACLI   string // tool paths ("" = not installed)
	Smartctl string
	Zpool    string

	smartCache   map[string]model.PhysDisk // serial -> SMART data for Smart Array disks
	smartCacheAt time.Time
}

// NewStorage detects installed tools.
func NewStorage() *Storage {
	return &Storage{
		Run: ExecRunner, SysRoot: "/sys", ProcRoot: "/proc",
		SSACLI: LookTool("ssacli"), Smartctl: LookTool("smartctl"), Zpool: LookTool("zpool"),
	}
}

// Collect gathers the storage report.
func (s *Storage) Collect(ctx context.Context, now time.Time) *model.StorageReport {
	r := &model.StorageReport{TS: now.UnixMilli(), Source: "none"}
	if s.SSACLI != "" {
		out, code, err := s.Run(ctx, s.SSACLI, "ctrl", "all", "show", "config", "detail")
		if err != nil || code != 0 {
			r.Errors = append(r.Errors, "ssacli: "+errText(err, code))
		} else {
			r.Source = "ssacli"
			r.Controllers, r.Logical, r.Physical = parseSSACLI(string(out))
			if len(r.Controllers) == 0 {
				r.Errors = append(r.Errors, "ssacli: kontroller topilmadi")
			}
			s.addSmartToSSACLI(ctx, r, now)
		}
	}
	if r.Source == "none" && s.Smartctl != "" {
		r.Source = "smartctl"
		s.smartDirect(ctx, r)
	}
	s.softwareRAID(ctx, r)
	if r.Source == "none" && len(r.Logical) == 0 {
		r.Errors = append(r.Errors, "ssacli va smartctl topilmadi")
	}
	sort.SliceStable(r.Physical, func(i, j int) bool { return natLess(r.Physical[i].ID, r.Physical[j].ID) })
	return r
}

func errText(err error, code int) string {
	if err != nil {
		return err.Error()
	}
	return "exit code " + strconv.Itoa(code)
}

// wholeDisks lists sd*/nvme*n* block devices that are real disks.
func (s *Storage) wholeDisks() []string {
	ents, _ := os.ReadDir(filepath.Join(s.SysRoot, "block"))
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !(strings.HasPrefix(n, "sd") || (strings.HasPrefix(n, "nvme") && strings.Contains(n, "n"))) {
			continue
		}
		if !wholeDisk(s.SysRoot, n) {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// smartDirect queries each whole disk; HPE logical volumes are probed with
// -d cciss,N to reach the physical disks behind the controller.
func (s *Storage) smartDirect(ctx context.Context, r *model.StorageReport) {
	for _, n := range s.wholeDisks() {
		dev := "/dev/" + n
		out, _, err := s.Run(ctx, s.Smartctl, "-a", "-j", dev)
		if err != nil {
			r.Errors = append(r.Errors, "smartctl "+dev+": "+err.Error())
			continue
		}
		d, logical, err := parseSmartJSON(out, dev)
		if err != nil {
			r.Errors = append(r.Errors, err.Error())
			continue
		}
		if !logical {
			r.Physical = append(r.Physical, d)
			continue
		}
		r.Logical = append(r.Logical, model.LogicalDrv{Controller: "Smart Array", ID: n, Device: dev, Status: "noma'lum (ssacli yo'q)", State: "unknown"})
		for i := 0; i < 32; i++ {
			id := "cciss," + strconv.Itoa(i)
			out, _, err := s.Run(ctx, s.Smartctl, "-a", "-j", "-d", id, dev)
			if err != nil {
				break
			}
			pd, _, err := parseSmartJSON(out, id)
			if err != nil {
				break // no more disks behind this controller
			}
			pd.Controller = "Smart Array"
			r.Physical = append(r.Physical, pd)
			r.Logical[len(r.Logical)-1].Members = append(r.Logical[len(r.Logical)-1].Members, id)
		}
	}
}

// addSmartToSSACLI fills power-on hours and SMART status of Smart Array
// disks via smartctl -d cciss,N, matched by serial number. Cached for
// 10 minutes because it costs one smartctl run per disk.
func (s *Storage) addSmartToSSACLI(ctx context.Context, r *model.StorageReport, now time.Time) {
	if s.Smartctl == "" {
		return
	}
	dev := ""
	for _, ld := range r.Logical {
		if ld.Device != "" {
			dev = ld.Device
			break
		}
	}
	if dev == "" {
		return
	}
	if s.smartCache == nil || now.Sub(s.smartCacheAt) > 10*time.Minute {
		cache := map[string]model.PhysDisk{}
		for i := 0; i < len(r.Physical)+2 && i < 64; i++ {
			out, _, err := s.Run(ctx, s.Smartctl, "-a", "-j", "-d", "cciss,"+strconv.Itoa(i), dev)
			if err != nil {
				break
			}
			pd, _, err := parseSmartJSON(out, "")
			if err != nil {
				break
			}
			if pd.Serial != "" {
				cache[strings.TrimSpace(pd.Serial)] = pd
			}
		}
		s.smartCache, s.smartCacheAt = cache, now
	}
	for i := range r.Physical {
		p := &r.Physical[i]
		if sm, ok := s.smartCache[strings.TrimSpace(p.Serial)]; ok {
			if p.PowerOnHours == 0 {
				p.PowerOnHours = sm.PowerOnHours
			}
			p.SMART = sm.SMART
			if sm.State == "failed" && p.State == "ok" {
				p.State, p.Status = "warning", p.Status+"; "+sm.Status
			}
		}
	}
}

var mdLine = regexp.MustCompile(`^(md\d+)\s*:\s*(\S+)\s+(?:\(\S+\)\s+)?(raid\d+|linear)\s+(.*)$`)
var mdStatus = regexp.MustCompile(`\[(\d+)/(\d+)\]\s+\[([U_]+)\]`)

// parseMdstat reads /proc/mdstat (Linux software RAID).
func parseMdstat(b string) []model.LogicalDrv {
	var out []model.LogicalDrv
	ls := strings.Split(b, "\n")
	for i, l := range ls {
		m := mdLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		ld := model.LogicalDrv{Controller: "mdadm", ID: m[1], Device: "/dev/" + m[1], RAID: strings.TrimPrefix(m[3], "raid"), Status: m[2], State: "ok"}
		for _, dev := range strings.Fields(m[4]) {
			name := dev
			if j := strings.IndexByte(dev, '['); j > 0 {
				name = dev[:j]
			}
			if strings.HasSuffix(dev, "(S)") {
				ld.Spares = append(ld.Spares, name)
			} else {
				ld.Members = append(ld.Members, name)
			}
		}
		if m[2] != "active" {
			ld.State = "failed"
		}
		for _, next := range ls[i+1 : min(i+4, len(ls))] {
			if st := mdStatus.FindStringSubmatch(next); st != nil {
				ld.Status = st[0]
				if strings.Contains(st[3], "_") {
					ld.State = "degraded"
				}
			}
			if strings.Contains(next, "recovery") || strings.Contains(next, "resync") {
				ld.State = "rebuilding"
				if p := progressRe.FindStringSubmatch(next); p != nil {
					ld.Progress, _ = strconv.ParseFloat(p[1], 64)
				}
			}
		}
		out = append(out, ld)
	}
	return out
}

// parseZpoolList reads `zpool list -H -p -o name,size,health`.
func parseZpoolList(b string) []model.LogicalDrv {
	var out []model.LogicalDrv
	for _, l := range strings.Split(strings.TrimSpace(b), "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 3 {
			continue
		}
		size, _ := strconv.ParseUint(f[1], 10, 64)
		ld := model.LogicalDrv{Controller: "ZFS", ID: f[0], RAID: "zfs", Size: size, Status: f[2]}
		switch f[2] {
		case "ONLINE":
			ld.State = "ok"
		case "DEGRADED":
			ld.State = "degraded"
		case "FAULTED", "UNAVAIL", "REMOVED", "SUSPENDED":
			ld.State = "failed"
		default:
			ld.State = "unknown"
		}
		out = append(out, ld)
	}
	return out
}

func (s *Storage) softwareRAID(ctx context.Context, r *model.StorageReport) {
	if b, err := os.ReadFile(filepath.Join(s.ProcRoot, "mdstat")); err == nil {
		r.Logical = append(r.Logical, parseMdstat(string(b))...)
	}
	if s.Zpool != "" {
		out, code, err := s.Run(ctx, s.Zpool, "list", "-H", "-p", "-o", "name,size,health")
		if err == nil && code == 0 {
			r.Logical = append(r.Logical, parseZpoolList(string(out))...)
		} else if err != nil {
			r.Errors = append(r.Errors, "zpool: "+err.Error())
		}
	}
}

// natLess orders "1I:1:2" before "1I:1:10".
func natLess(a, b string) bool {
	ra, rb := splitNum(a), splitNum(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		if ra[i] != rb[i] {
			na, ea := strconv.Atoi(ra[i])
			nb, eb := strconv.Atoi(rb[i])
			if ea == nil && eb == nil {
				return na < nb
			}
			return ra[i] < rb[i]
		}
	}
	return len(ra) < len(rb)
}

var numSplit = regexp.MustCompile(`\d+|\D+`)

func splitNum(s string) []string { return numSplit.FindAllString(s, -1) }
