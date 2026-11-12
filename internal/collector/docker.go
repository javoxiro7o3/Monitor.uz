package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"monitor.uz/internal/model"
)

// Docker lists containers through the Docker socket (GET requests only)
// and reads their resource usage from cgroup files; when cgroup v2 files
// are not found (cgroup v1 guests) it falls back to the stats API.
type Docker struct {
	Socket    string // /var/run/docker.sock
	CgroupDir string
	ProcRoot  string

	client   *http.Client
	inspect  map[string]inspectInfo // full id -> details (refreshed every minute)
	inspAt   map[string]time.Time
	prev     map[string]ctrCounters
	prevMono time.Time
}

type inspectInfo struct {
	Pid      int
	Restarts int
	ExitCode int
	Health   string
	MemLimit uint64
	Started  int64
}

type ctrCounters struct {
	cpuUsec, rbytes, wbytes, rx, tx uint64
	viaAPI                          bool
}

// NewDocker returns a collector for the default socket.
func NewDocker() *Docker {
	return &Docker{Socket: "/var/run/docker.sock", CgroupDir: "/sys/fs/cgroup", ProcRoot: "/proc"}
}

// Available reports whether the Docker socket exists.
func (d *Docker) Available() bool {
	_, err := os.Stat(d.Socket)
	return err == nil
}

func (d *Docker) http() *http.Client {
	if d.client == nil {
		sock := d.Socket
		d.client = &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var dl net.Dialer
					return dl.DialContext(ctx, "unix", sock)
				},
				MaxIdleConns: 2,
			},
		}
	}
	return d.client
}

func (d *Docker) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("docker %s: %s %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v)
}

type dockerListItem struct {
	ID     string   `json:"Id"`
	Names  []string `json:"Names"`
	Image  string   `json:"Image"`
	State  string   `json:"State"`
	Status string   `json:"Status"`
}

type dockerInspect struct {
	RestartCount int `json:"RestartCount"`
	State        struct {
		Pid       int    `json:"Pid"`
		ExitCode  int    `json:"ExitCode"`
		StartedAt string `json:"StartedAt"`
		Health    *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	HostConfig struct {
		Memory uint64 `json:"Memory"`
	} `json:"HostConfig"`
}

// Collect lists containers with their resource usage.
func (d *Docker) Collect(ctx context.Context, now time.Time) *model.DockerReport {
	r := &model.DockerReport{TS: now.UnixMilli()}
	var list []dockerListItem
	if err := d.get(ctx, "/containers/json?all=1", &list); err != nil {
		r.Error = err.Error()
		return r
	}
	if d.inspect == nil {
		d.inspect, d.inspAt, d.prev = map[string]inspectInfo{}, map[string]time.Time{}, map[string]ctrCounters{}
	}
	elapsed := 0.0
	if !d.prevMono.IsZero() {
		elapsed = now.Sub(d.prevMono).Seconds()
	}
	cur := map[string]ctrCounters{}
	alive := map[string]bool{}
	var apiNeeded []int
	for _, it := range list {
		alive[it.ID] = true
		c := model.Container{
			ID: shortID(it.ID), Image: it.Image, State: it.State, Status: it.Status,
		}
		if len(it.Names) > 0 {
			c.Name = strings.TrimPrefix(it.Names[0], "/")
		}
		// Inspect is refreshed every minute or when the state changes.
		info, ok := d.inspect[it.ID]
		if !ok || now.Sub(d.inspAt[it.ID]) > time.Minute || (it.State == "running") != (info.Pid > 0) {
			var ins dockerInspect
			if err := d.get(ctx, "/containers/"+it.ID+"/json", &ins); err == nil {
				info = inspectInfo{Pid: ins.State.Pid, Restarts: ins.RestartCount, ExitCode: ins.State.ExitCode, MemLimit: ins.HostConfig.Memory}
				if ins.State.Health != nil {
					info.Health = ins.State.Health.Status
				}
				if t, err := time.Parse(time.RFC3339Nano, ins.State.StartedAt); err == nil && t.Year() > 1 {
					info.Started = t.Unix()
				}
				d.inspect[it.ID], d.inspAt[it.ID] = info, now
			}
		}
		c.Restarts, c.ExitCode, c.Health, c.MemLimit, c.Started = info.Restarts, info.ExitCode, info.Health, info.MemLimit, info.Started

		if it.State == "running" {
			cg := d.cgroupPath(it.ID)
			if cg != "" {
				cc := ctrCounters{}
				if b, err := os.ReadFile(filepath.Join(cg, "cpu.stat")); err == nil {
					cc.cpuUsec = parseKV(b)["usage_usec"]
				}
				if b, err := os.ReadFile(filepath.Join(cg, "memory.current")); err == nil {
					used, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
					// Like `docker stats`: exclude reclaimable page cache.
					if ms, err := os.ReadFile(filepath.Join(cg, "memory.stat")); err == nil {
						if inact := parseKV(ms)["inactive_file"]; inact < used {
							used -= inact
						}
					}
					c.MemUsed = used
				}
				if b, err := os.ReadFile(filepath.Join(cg, "io.stat")); err == nil {
					cc.rbytes, cc.wbytes = parseIOStat(b)
				}
				if info.Pid > 0 {
					if b, err := os.ReadFile(filepath.Join(d.ProcRoot, strconv.Itoa(info.Pid), "net/dev")); err == nil {
						if nd, err := parseNetDev(b); err == nil {
							for name, n := range nd {
								if name != "lo" {
									cc.rx += n.RxBytes
									cc.tx += n.TxBytes
								}
							}
						}
					}
				}
				cur[it.ID] = cc
				if p, ok := d.prev[it.ID]; ok && elapsed > 0 && !p.viaAPI {
					if dt, ok := counterDelta(p.cpuUsec, cc.cpuUsec); ok {
						c.CPU = round2(float64(dt) / 1e6 / elapsed * 100)
					}
					c.ReadBps, _ = rate(p.rbytes, cc.rbytes, elapsed)
					c.WriteBps, _ = rate(p.wbytes, cc.wbytes, elapsed)
					c.NetRxBps, _ = rate(p.rx, cc.rx, elapsed)
					c.NetTxBps, _ = rate(p.tx, cc.tx, elapsed)
				}
			} else {
				apiNeeded = append(apiNeeded, len(r.Containers))
			}
		}
		r.Containers = append(r.Containers, c)
	}
	if len(apiNeeded) > 0 {
		d.statsViaAPI(ctx, list, r, apiNeeded)
	}
	for id := range d.inspect {
		if !alive[id] {
			delete(d.inspect, id)
			delete(d.inspAt, id)
		}
	}
	d.prev, d.prevMono = cur, now
	sort.Slice(r.Containers, func(i, j int) bool { return r.Containers[i].Name < r.Containers[j].Name })
	return r
}

// cgroupPath finds a container's cgroup v2 directory for the systemd or
// cgroupfs driver.
func (d *Docker) cgroupPath(id string) string {
	for _, p := range []string{
		filepath.Join(d.CgroupDir, "system.slice", "docker-"+id+".scope"),
		filepath.Join(d.CgroupDir, "docker", id),
	} {
		if _, err := os.Stat(filepath.Join(p, "cpu.stat")); err == nil {
			return p
		}
	}
	return ""
}

type dockerStats struct {
	CPUStats struct {
		CPUUsage struct {
			Total uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		System uint64 `json:"system_cpu_usage"`
		Online int    `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			Total uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		System uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		Rx uint64 `json:"rx_bytes"`
		Tx uint64 `json:"tx_bytes"`
	} `json:"networks"`
}

// statsViaAPI is the cgroup v1 fallback: one-shot stats (CPU% from the
// API's own pre/cur pair; network/IO rates are not computed here).
func (d *Docker) statsViaAPI(ctx context.Context, list []dockerListItem, r *model.DockerReport, idx []int) {
	byShort := map[string]string{}
	for _, it := range list {
		byShort[shortID(it.ID)] = it.ID
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, i := range idx {
		wg.Add(1)
		go func(c *model.Container) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var st dockerStats
			if err := d.get(ctx, "/containers/"+byShort[c.ID]+"/stats?stream=false", &st); err != nil {
				return
			}
			cpuD := float64(st.CPUStats.CPUUsage.Total) - float64(st.PreCPUStats.CPUUsage.Total)
			sysD := float64(st.CPUStats.System) - float64(st.PreCPUStats.System)
			if sysD > 0 && cpuD >= 0 {
				n := st.CPUStats.Online
				if n == 0 {
					n = 1
				}
				c.CPU = round2(cpuD / sysD * float64(n) * 100)
			}
			used := st.MemoryStats.Usage
			for _, k := range []string{"inactive_file", "total_inactive_file"} {
				if v, ok := st.MemoryStats.Stats[k]; ok && v < used {
					used -= v
					break
				}
			}
			c.MemUsed = used
			if c.MemLimit == 0 {
				c.MemLimit = st.MemoryStats.Limit
			}
		}(&r.Containers[i])
	}
	wg.Wait()
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
