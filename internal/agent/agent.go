package agent

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"monitor.uz/internal/collector"
	"monitor.uz/internal/model"
)

// Config controls the agent loop.
type Config struct {
	StateDir   string
	Interval   time.Duration // base sample interval (host 1 s, guest 5 s)
	BufferSize int           // max samples kept while the hub is unreachable
	BatchSize  int           // max samples per request
	Version    string
	Mode       string // host | guest

	VMInterval      time.Duration // host: VM/CT resources (5 s)
	SensorsInterval time.Duration // host: temperatures/fans/PSU (10 s)
	StorageInterval time.Duration // host: disks/RAID (60 s)
	NetInterval     time.Duration // host: interface/bridge layout (60 s)
	GuestInterval   time.Duration // guest: processes + Docker (5 s)

	// OnUpdate is called with a verified new binary path; it must replace
	// the running binary. Run returns ErrRestart afterwards.
	OnUpdate func(newBinary string, order *model.UpdateOrder) error
}

// ErrRestart asks the caller to exit so systemd starts the new binary.
var ErrRestart = errors.New("restart requested after update")

// Defaults fills unset intervals.
func (c *Config) Defaults() {
	if c.Interval <= 0 {
		c.Interval = time.Second
		if c.Mode == "guest" {
			c.Interval = 5 * time.Second
		}
	}
	set := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	set(&c.VMInterval, 5*time.Second)
	set(&c.SensorsInterval, 10*time.Second)
	set(&c.StorageInterval, 60*time.Second)
	set(&c.NetInterval, 60*time.Second)
	set(&c.GuestInterval, 5*time.Second)
	if c.BufferSize <= 0 {
		c.BufferSize = 1800
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 300
	}
}

// pending holds the newest slow reports not yet delivered.
type pending struct {
	mu sync.Mutex
	r  model.Reports
}

func (p *pending) set(f func(r *model.Reports)) {
	p.mu.Lock()
	f(&p.r)
	p.mu.Unlock()
}

// take returns the pending reports (nil if none).
func (p *pending) take() *model.Reports {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.r
	if r == (model.Reports{}) {
		return nil
	}
	p.r = model.Reports{}
	return &r
}

// putBack restores reports after a failed delivery unless newer ones
// arrived meanwhile.
func (p *pending) putBack(r *model.Reports) {
	if r == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.r.VMs == nil {
		p.r.VMs = r.VMs
	}
	if p.r.Net == nil {
		p.r.Net = r.Net
	}
	if p.r.Storage == nil {
		p.r.Storage = r.Storage
	}
	if p.r.Sensors == nil {
		p.r.Sensors = r.Sensors
	}
	if p.r.Procs == nil {
		p.r.Procs = r.Procs
	}
	if p.r.Docker == nil {
		p.r.Docker = r.Docker
	}
}

// every runs fn immediately and then on each tick until ctx ends.
func every(ctx context.Context, wg *sync.WaitGroup, d time.Duration, fn func(now time.Time)) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("collector panic", "err", r) // never take the agent down
			}
		}()
		fn(time.Now())
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				fn(time.Now())
			}
		}
	}()
}

// BuildInfo describes the machine for the hub.
func BuildInfo(mode, version string) model.Info {
	inf := collector.ReadInfo("/proc", mode, version)
	inf.Arch = runtime.GOARCH
	for _, t := range []string{"ssacli", "smartctl", "ipmitool", "zpool"} {
		if collector.LookTool(t) != "" {
			inf.Tools = append(inf.Tools, t)
		}
	}
	if mode == "guest" {
		inf.Ifaces = collector.GuestIfaces()
		if collector.NewDocker().Available() {
			inf.Tools = append(inf.Tools, "docker")
		}
	}
	if mode == "host" && collector.IsProxmox("/etc/pve") {
		inf.Tools = append(inf.Tools, "proxmox")
	}
	return inf
}

// Run collects and pushes until ctx is cancelled. Undelivered samples are
// saved to disk on exit.
func Run(ctx context.Context, cfg Config, st *State, log *slog.Logger) error {
	cfg.Defaults()
	client, err := NewClient(st.Hub, st.Pin, st.Token)
	if err != nil {
		return err
	}
	buf := NewBuffer(cfg.BufferSize)
	bufFile := filepath.Join(cfg.StateDir, "buffer.json.gz")
	if n, err := buf.Load(bufFile); err != nil {
		log.Warn("could not restore buffer", "err", err)
	} else if n > 0 {
		log.Info("restored buffered samples", "count", n)
	}

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	pend := &pending{}

	if cfg.Mode == "host" {
		if collector.IsProxmox("/etc/pve") {
			pve := collector.NewProxmox()
			every(cctx, &wg, cfg.VMInterval, func(now time.Time) {
				r := pve.Collect(now)
				pend.set(func(x *model.Reports) { x.VMs = r })
			})
		} else {
			log.Info("no /etc/pve: VM/CT collection disabled")
		}
		every(cctx, &wg, cfg.NetInterval, func(now time.Time) {
			r := collector.HostNet("/sys", now)
			pend.set(func(x *model.Reports) { x.Net = r })
		})
		sens := collector.NewSensors(cfg.StateDir)
		every(cctx, &wg, cfg.SensorsInterval, func(now time.Time) {
			r := sens.Collect(cctx, now)
			pend.set(func(x *model.Reports) { x.Sensors = r })
		})
		stor := collector.NewStorage()
		every(cctx, &wg, cfg.StorageInterval, func(now time.Time) {
			r := stor.Collect(cctx, now)
			pend.set(func(x *model.Reports) { x.Storage = r })
		})
	} else {
		procs := collector.NewProcs()
		dock := collector.NewDocker()
		every(cctx, &wg, cfg.GuestInterval, func(now time.Time) {
			r := procs.Collect(now)
			pend.set(func(x *model.Reports) { x.Procs = r })
			if dock.Available() {
				d := dock.Collect(cctx, now)
				pend.set(func(x *model.Reports) { x.Docker = d })
			}
		})
	}

	col := collector.NewHost()
	wake := make(chan struct{}, 1)
	sendErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		sendErr <- sendLoop(cctx, cfg, client, buf, pend, wake, log)
	}()

	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	col.Collect(time.Now()) // prime counters; first sample has no rates
	var result error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case err := <-sendErr:
			result = err // update installed
			break loop
		case <-t.C:
			// time.Now() carries a monotonic reading; Collect divides
			// counter deltas by the real elapsed time, not by Interval.
			buf.Push(col.Collect(time.Now()))
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}
	cancel()
	wg.Wait()
	if err := buf.Save(bufFile); err != nil {
		log.Error("saving buffer failed", "err", err)
	} else if n := buf.Len(); n > 0 {
		log.Info("saved undelivered samples", "count", n)
	}
	return result
}

func sendLoop(ctx context.Context, cfg Config, c *Client, buf *Buffer, pend *pending, wake <-chan struct{}, log *slog.Logger) error {
	info := BuildInfo(cfg.Mode, cfg.Version)
	infoAt := time.Now()
	var infoSent time.Time
	backoff := time.Duration(0)
	failing := false
	for {
		if backoff > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
		} else {
			select {
			case <-ctx.Done():
				return nil
			case <-wake:
			}
		}
		if time.Since(infoAt) > 10*time.Minute { // IPs/tools may change
			info, infoAt = BuildInfo(cfg.Mode, cfg.Version), time.Now()
			infoSent = time.Time{}
		}
		for buf.Len() > 0 {
			batch := buf.Peek(cfg.BatchSize)
			req := &model.IngestRequest{Samples: batch, Reports: pend.take()}
			if time.Since(infoSent) > time.Minute {
				inf := info
				req.Info = &inf
			}
			rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			resp, err := c.Ingest(rctx, req)
			cancel()
			if err != nil {
				pend.putBack(req.Reports)
				if ctx.Err() != nil {
					return nil
				}
				backoff = nextBackoff(backoff, errors.Is(err, ErrUnauthorized))
				if !failing {
					log.Warn("hub unreachable, buffering", "err", err, "buffered", buf.Len())
					failing = true
				}
				break
			}
			if req.Info != nil {
				infoSent = time.Now()
			}
			buf.Ack(batch[len(batch)-1].TS)
			if failing {
				log.Info("hub reachable again", "pending", buf.Len(), "dropped_total", buf.Dropped())
				failing = false
			}
			backoff = 0
			if resp != nil && resp.Update != nil && resp.Update.Version != cfg.Version && cfg.OnUpdate != nil {
				if err := applyUpdate(ctx, c, cfg, resp.Update, log); err != nil {
					log.Error("update failed", "version", resp.Update.Version, "err", err)
				} else {
					return ErrRestart
				}
			}
		}
	}
}

// nextBackoff grows 1s,2s,4s… up to 10s (5 min when the token is rejected,
// so a revoked agent does not hammer the hub).
func nextBackoff(cur time.Duration, unauthorized bool) time.Duration {
	if unauthorized {
		return 5 * time.Minute
	}
	if cur <= 0 {
		return time.Second
	}
	return min(cur*2, 10*time.Second)
}
