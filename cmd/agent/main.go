// Command monitor-agent collects metrics and pushes them to the hub.
// It only reads the system and never opens a listening port.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"monitor.uz/internal/agent"
	"monitor.uz/internal/collector"
	"monitor.uz/internal/model"
)

var version = "dev"

func usage() {
	fmt.Fprint(os.Stderr, `Monitor.uz agent `+version+`

Foydalanish:
  monitor-agent run     --mode=host [--state-dir DIR]
  monitor-agent enroll  --hub URL --pin PIN --token TOKEN [--mode host] [--state-dir DIR]
  monitor-agent collect                  bitta namunani JSON ko'rinishida chiqaradi (tekshirish uchun)
  monitor-agent version
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "run":
		err = cmdRun(args)
	case "enroll":
		err = cmdEnroll(args)
	case "collect":
		err = cmdCollect()
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "XATO:", err)
		os.Exit(1)
	}
}

func checkMode(mode string) error {
	switch mode {
	case "host":
		return nil
	case "guest":
		return nil
	}
	return fmt.Errorf("noma'lum rejim %q (host yoki guest)", mode)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	mode := fs.String("mode", "host", "host yoki guest")
	stateDir := fs.String("state-dir", "/var/lib/monitor-agent", "holat katalogi")
	interval := fs.Duration("interval", 0, "asosiy yig'ish davri (host 1s, guest 5s)")
	bufSize := fs.Int("buffer", 1800, "hub o'chiq paytda saqlanadigan namunalar soni")
	vmIv := fs.Duration("vm-interval", 5*time.Second, "VM/CT resurslari davri")
	sensIv := fs.Duration("sensors-interval", 10*time.Second, "harorat/fan/PSU davri")
	storIv := fs.Duration("storage-interval", 60*time.Second, "disk/RAID davri")
	fs.Parse(args)
	if err := checkMode(*mode); err != nil {
		return err
	}

	// Keep the agent light: few OS threads, soft memory cap.
	runtime.GOMAXPROCS(2)
	limit := int64(40 << 20)
	if *mode == "guest" {
		limit = 25 << 20
	}
	debug.SetMemoryLimit(limit)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log)
	st, err := agent.LoadState(*stateDir)
	if err != nil {
		return fmt.Errorf("agent ro'yxatdan o'tmagan (%v): avval 'monitor-agent enroll' ni bajaring", err)
	}
	self, err := os.Executable()
	if err == nil {
		self, _ = filepath.EvalSymlinks(self)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("agent started", "version", version, "mode", *mode, "hub", st.Hub, "agent_id", st.AgentID)
	err = agent.Run(ctx, agent.Config{
		StateDir: *stateDir, Interval: *interval, BufferSize: *bufSize,
		Version: version, Mode: *mode,
		VMInterval: *vmIv, SensorsInterval: *sensIv, StorageInterval: *storIv,
		OnUpdate: func(newBin string, o *model.UpdateOrder) error {
			if self == "" {
				return errors.New("own path unknown")
			}
			return agent.ReplaceBinary(self, newBin)
		},
	}, st, log)
	if errors.Is(err, agent.ErrRestart) {
		log.Info("exiting so systemd starts the updated binary")
		return nil
	}
	return err
}

func cmdEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	hub := fs.String("hub", "", "hub manzili, masalan https://10.0.0.5:8443")
	pin := fs.String("pin", "", "hub sertifikati pin'i")
	token := fs.String("token", "", "bir martalik o'rnatish tokeni")
	mode := fs.String("mode", "host", "host yoki guest")
	stateDir := fs.String("state-dir", "/var/lib/monitor-agent", "holat katalogi")
	fs.Parse(args)
	if *hub == "" || *pin == "" || *token == "" {
		return errors.New("--hub, --pin va --token majburiy")
	}
	if err := checkMode(*mode); err != nil {
		return err
	}
	c, err := agent.NewClient(*hub, *pin, "")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info := agent.BuildInfo(*mode, version)
	resp, err := c.Enroll(ctx, *token, info)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*stateDir, 0o750); err != nil {
		return err
	}
	if err := agent.SaveState(*stateDir, &agent.State{Hub: *hub, Pin: *pin, AgentID: resp.AgentID, Token: resp.Token, Mode: *mode}); err != nil {
		return err
	}
	fmt.Printf("Ro'yxatdan o'tildi: agent ID %d (%s)\n", resp.AgentID, info.Hostname)
	return nil
}

func cmdCollect() error {
	ctx := context.Background()
	h := collector.NewHost()
	pve := collector.NewProxmox()
	procs := collector.NewProcs()
	dock := collector.NewDocker()
	isPVE := collector.IsProxmox("/etc/pve")
	t0 := time.Now()
	h.Collect(t0)
	if isPVE {
		pve.Collect(t0)
	}
	procs.Collect(t0)
	if dock.Available() {
		dock.Collect(ctx, t0)
	}
	time.Sleep(time.Second)
	now := time.Now()
	out := map[string]any{"info": agent.BuildInfo("host", version), "sample": h.Collect(now)}
	if isPVE {
		out["vms"] = pve.Collect(now)
	}
	out["net"] = collector.HostNet("/sys", now)
	out["storage"] = collector.NewStorage().Collect(ctx, now)
	out["sensors"] = collector.NewSensors(os.TempDir()).Collect(ctx, now)
	out["procs"] = procs.Collect(now)
	if dock.Available() {
		out["docker"] = dock.Collect(ctx, now)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
