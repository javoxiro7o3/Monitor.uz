package collector

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"monitor.uz/internal/model"
)

// Procs reports the busiest processes. /proc/<pid> is scanned every 5 s
// (never every second) and only the top N are sent.
type Procs struct {
	ProcRoot string
	TopN     int

	prev     map[int]uint64 // pid -> utime+stime ticks
	prevMono time.Time
	users    map[string]string
	usersAt  time.Time
}

// NewProcs returns a process collector.
func NewProcs() *Procs { return &Procs{ProcRoot: "/proc", TopN: 15} }

type pidStat struct {
	pid   int
	name  string
	ticks uint64
	rss   uint64 // pages
}

// parsePidStat parses /proc/<pid>/stat. comm may contain spaces and ')'.
func parsePidStat(b []byte) (pidStat, bool) {
	var p pidStat
	open := bytes.IndexByte(b, '(')
	end := bytes.LastIndexByte(b, ')')
	if open < 0 || end < open {
		return p, false
	}
	pid, err := strconv.Atoi(string(bytes.TrimSpace(b[:open])))
	if err != nil {
		return p, false
	}
	p.pid = pid
	p.name = string(b[open+1 : end])
	f := bytes.Fields(b[end+1:])
	// f[0]=state(3) ... utime(14)=f[11], stime(15)=f[12], rss(24)=f[21]
	if len(f) < 22 {
		return p, false
	}
	ut, _ := parseUint(f[11])
	st, _ := parseUint(f[12])
	rss, _ := parseUint(f[21])
	p.ticks, p.rss = ut+st, rss
	return p, true
}

// Collect scans processes.
func (c *Procs) Collect(now time.Time) *model.ProcReport {
	r := &model.ProcReport{TS: now.UnixMilli()}
	ents, err := os.ReadDir(c.ProcRoot)
	if err != nil {
		return r
	}
	elapsed := 0.0
	if !c.prevMono.IsZero() {
		elapsed = now.Sub(c.prevMono).Seconds()
	}
	page := uint64(os.Getpagesize())
	cur := make(map[int]uint64, len(ents))
	var all []model.Proc
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.ProcRoot, e.Name(), "stat"))
		if err != nil {
			continue // process exited
		}
		ps, ok := parsePidStat(b)
		if !ok || ps.rss == 0 { // kernel threads have no RSS
			continue
		}
		cur[ps.pid] = ps.ticks
		p := model.Proc{PID: ps.pid, Name: ps.name, RSS: ps.rss * page}
		if prev, ok := c.prev[ps.pid]; ok && elapsed > 0 && ps.ticks >= prev {
			p.CPU = round2(float64(ps.ticks-prev) / clockTicks / elapsed * 100)
		}
		all = append(all, p)
	}
	r.Total = len(all)
	c.prev, c.prevMono = cur, now

	// Top by CPU, then make sure the largest memory users are included.
	sort.Slice(all, func(i, j int) bool {
		if all[i].CPU != all[j].CPU {
			return all[i].CPU > all[j].CPU
		}
		return all[i].RSS > all[j].RSS
	})
	n := min(c.TopN, len(all))
	top := append([]model.Proc(nil), all[:n]...)
	byMem := append([]model.Proc(nil), all...)
	sort.Slice(byMem, func(i, j int) bool { return byMem[i].RSS > byMem[j].RSS })
	seen := map[int]bool{}
	for _, p := range top {
		seen[p.PID] = true
	}
	for _, p := range byMem[:min(5, len(byMem))] {
		if !seen[p.PID] {
			top = append(top, p)
		}
	}
	users := c.userMap(now)
	for i := range top {
		if b, err := os.ReadFile(filepath.Join(c.ProcRoot, strconv.Itoa(top[i].PID), "status")); err == nil {
			lines(b, func(l []byte) {
				if v, ok := bytes.CutPrefix(l, []byte("Uid:")); ok {
					if f := bytes.Fields(v); len(f) > 0 {
						uid := string(f[0])
						if u, ok := users[uid]; ok {
							top[i].User = u
						} else {
							top[i].User = uid
						}
					}
				}
			})
		}
	}
	r.Top = top
	return r
}

func (c *Procs) userMap(now time.Time) map[string]string {
	if c.users != nil && now.Sub(c.usersAt) < 10*time.Minute {
		return c.users
	}
	m := map[string]string{}
	if b, err := os.ReadFile("/etc/passwd"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Split(l, ":")
			if len(f) > 3 {
				m[f[2]] = f[0]
			}
		}
	}
	c.users, c.usersAt = m, now
	return m
}
