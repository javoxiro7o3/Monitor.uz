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
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"monitor.uz/internal/agent"
	"monitor.uz/internal/collector"
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
		return errors.New("guest rejimi 4-bosqichda qo'shiladi")
	}
	return fmt.Errorf("noma'lum rejim %q (host yoki guest)", mode)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	mode := fs.String("mode", "host", "host yoki guest")
	stateDir := fs.String("state-dir", "/var/lib/monitor-agent", "holat katalogi")
	interval := fs.Duration("interval", time.Second, "yig'ish davri")
	bufSize := fs.Int("buffer", 1800, "hub o'chiq paytda saqlanadigan namunalar soni")
	fs.Parse(args)
	if err := checkMode(*mode); err != nil {
		return err
	}

	// Keep the agent light: few OS threads, soft memory cap.
	runtime.GOMAXPROCS(2)
	debug.SetMemoryLimit(40 << 20)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := agent.LoadState(*stateDir)
	if err != nil {
		return fmt.Errorf("agent ro'yxatdan o'tmagan (%v): avval 'monitor-agent enroll' ni bajaring", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("agent started", "version", version, "mode", *mode, "hub", st.Hub, "agent_id", st.AgentID)
	return agent.Run(ctx, agent.Config{
		StateDir: *stateDir, Interval: *interval, BufferSize: *bufSize, BatchSize: 300,
		Version: version, Mode: *mode,
	}, st, log)
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
	info := collector.ReadInfo("/proc", *mode, version)
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
	h := collector.NewHost()
	h.Collect(time.Now())
	time.Sleep(time.Second)
	s := h.Collect(time.Now())
	out := map[string]any{"info": collector.ReadInfo("/proc", "host", version), "sample": s}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
