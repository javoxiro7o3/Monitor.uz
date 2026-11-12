// Command monitor-hub receives agent data, stores history and serves the
// Monitor.uz panel.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"monitor.uz/internal/api"
	"monitor.uz/internal/store"
	"monitor.uz/internal/tlsutil"
	"monitor.uz/web"
)

var version = "dev"

func usage() {
	fmt.Fprint(os.Stderr, `Monitor.uz hub `+version+`

Foydalanish:
  monitor-hub serve   [--data DIR] [--listen :8443] [--url https://IP:8443] [--san IP,NOM]
  monitor-hub token   [--data DIR] [--ttl 24h] [--note srv-01]   bir martalik o'rnatish tokeni
  monitor-hub agents  [--data DIR]                                agentlar ro'yxati
  monitor-hub revoke  [--data DIR] ID                             agent tokenini bekor qilish
  monitor-hub pin     [--data DIR]                                sertifikat pin'i
  monitor-hub version

Sozlamalarni muhit o'zgaruvchilari orqali ham berish mumkin:
  MONITOR_HUB_LISTEN, MONITOR_HUB_URL, MONITOR_HUB_SAN, MONITOR_HUB_HISTORY
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
	case "serve":
		err = cmdServe(args)
	case "token":
		err = cmdToken(args)
	case "agents":
		err = cmdAgents(args)
	case "revoke":
		err = cmdRevoke(args)
	case "pin":
		err = cmdPin(args)
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

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func dataFlag(fs *flag.FlagSet) *string {
	return fs.String("data", env("MONITOR_HUB_DATA", "/var/lib/monitor-hub"), "ma'lumotlar katalogi")
}

func paths(data string) (db, cert, key, dist string) {
	return filepath.Join(data, "hub.db"), filepath.Join(data, "hub.crt"), filepath.Join(data, "hub.key"), filepath.Join(data, "dist")
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	data := dataFlag(fs)
	listen := fs.String("listen", env("MONITOR_HUB_LISTEN", ":8443"), "tinglash manzili")
	pubURL := fs.String("url", env("MONITOR_HUB_URL", ""), "agentlar ulanadigan manzil, masalan https://10.0.0.5:8443")
	san := fs.String("san", env("MONITOR_HUB_SAN", ""), "sertifikatga qo'shimcha IP/nomlar (vergul bilan)")
	certFile := fs.String("tls-cert", env("MONITOR_HUB_TLS_CERT", ""), "tashqi sertifikat (ixtiyoriy)")
	keyFile := fs.String("tls-key", env("MONITOR_HUB_TLS_KEY", ""), "tashqi kalit (ixtiyoriy)")
	history := fs.Duration("history", envDuration("MONITOR_HUB_HISTORY", 7*24*time.Hour), "tarix saqlash muddati")
	fs.Parse(args)

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := os.MkdirAll(*data, 0o750); err != nil {
		return err
	}
	dbPath, defCert, defKey, dist := paths(*data)
	if err := os.MkdirAll(dist, 0o750); err != nil {
		return err
	}

	cert, key := *certFile, *keyFile
	if cert == "" {
		cert, key = defCert, defKey
		hosts := tlsutil.LocalHosts()
		if *san != "" {
			hosts = append(hosts, strings.Split(*san, ",")...)
		}
		if u, err := url.Parse(*pubURL); err == nil && u.Hostname() != "" {
			hosts = append(hosts, u.Hostname())
		}
		created, err := tlsutil.EnsureSelfSigned(cert, key, hosts)
		if err != nil {
			return fmt.Errorf("certificate: %w", err)
		}
		if created {
			log.Info("self-signed certificate created", "cert", cert, "names", hosts)
		}
	}
	pin, err := tlsutil.PinFromFile(cert)
	if err != nil {
		return err
	}
	tlsCert, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return err
	}

	if *pubURL == "" {
		*pubURL = guessURL(*listen)
		log.Warn("--url not set; install script will use a guessed address", "url", *pubURL)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	st.HistoryRetention = *history
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// The CLI (token command) reads these to print the install command.
	st.SetSetting(ctx, "public_url", *pubURL)
	st.SetSetting(ctx, "pin", pin)

	webFS, err := web.Files()
	if err != nil {
		return err
	}
	srv, err := api.New(ctx, api.Config{
		Version: version, PublicURL: *pubURL, Pin: pin, DistDir: dist,
		OfflineTimeout: 30 * time.Second, Web: webFS,
	}, st, log)
	if err != nil {
		return err
	}
	go srv.Background(ctx)

	hs := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{tlsCert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	log.Info("hub started", "version", version, "listen", *listen, "url", *pubURL, "pin", pin, "db", dbPath)
	if err := hs.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("hub stopped")
	return nil
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func guessURL(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		port = "8443"
	}
	for _, h := range tlsutil.LocalHosts() {
		if ip := net.ParseIP(h); ip != nil && ip.To4() != nil && !ip.IsLoopback() {
			return "https://" + net.JoinHostPort(h, port)
		}
	}
	return "https://" + net.JoinHostPort("127.0.0.1", port)
}

// openForCLI refuses to run as root against a database owned by the
// service user: SQLite could create root-owned -wal/-shm files that the
// hub then cannot write.
func openForCLI(data string) (*store.Store, error) {
	dbPath, _, _, _ := paths(data)
	fi, err := os.Stat(dbPath)
	if err != nil {
		return nil, fmt.Errorf("baza topilmadi (%s): hub hali ishga tushmaganmi? %w", dbPath, err)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 && st.Uid != 0 {
		return nil, errors.New("root sifatida emas, xizmat foydalanuvchisi nomidan ishga tushiring: sudo -u monitor-hub monitor-hub ...")
	}
	return store.Open(dbPath)
}

func cmdToken(args []string) error {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	data := dataFlag(fs)
	ttl := fs.Duration("ttl", 24*time.Hour, "token amal qilish muddati")
	note := fs.String("note", "", "izoh (masalan, server nomi)")
	fs.Parse(args)
	st, err := openForCLI(*data)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	tok, exp, err := st.CreateInstallToken(ctx, *ttl, *note)
	if err != nil {
		return err
	}
	u, _ := st.Setting(ctx, "public_url")
	pin, _ := st.Setting(ctx, "pin")
	fmt.Printf("Bir martalik o'rnatish tokeni (%s gacha amal qiladi):\n  %s\n\n", exp.Format("2006-01-02 15:04"), tok)
	if u != "" && pin != "" {
		fmt.Printf("Proxmox hostda root sifatida ishga tushiring:\n  curl -fsS --insecure --pinnedpubkey 'sha256//%s' %s/install.sh | sh -s -- --token %s\n", pin, u, tok)
	}
	return nil
}

func cmdAgents(args []string) error {
	fs := flag.NewFlagSet("agents", flag.ExitOnError)
	data := dataFlag(fs)
	fs.Parse(args)
	st, err := openForCLI(*data)
	if err != nil {
		return err
	}
	defer st.Close()
	agents, err := st.Agents(context.Background())
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNOMI\tREJIM\tVERSIYA\tOXIRGI ALOQA\tHOLAT")
	for _, a := range agents {
		seen := "-"
		if a.LastSeen > 0 {
			seen = time.UnixMilli(a.LastSeen).Format("2006-01-02 15:04:05")
		}
		state := "faol"
		if a.Revoked {
			state = "bekor qilingan"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", a.ID, a.Name, a.Mode, a.Info.Version, seen, state)
	}
	return tw.Flush()
}

func cmdRevoke(args []string) error {
	fs := flag.NewFlagSet("revoke", flag.ExitOnError)
	data := dataFlag(fs)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("agent ID kerak")
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return err
	}
	st, err := openForCLI(*data)
	if err != nil {
		return err
	}
	defer st.Close()
	ok, err := st.RevokeAgent(context.Background(), id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("agent %d topilmadi", id)
	}
	fmt.Printf("Agent %d tokeni bekor qilindi. Hub qayta ishga tushirilganda panelda ham ko'rinadi.\n", id)
	return nil
}

func cmdPin(args []string) error {
	fs := flag.NewFlagSet("pin", flag.ExitOnError)
	data := dataFlag(fs)
	fs.Parse(args)
	_, cert, _, _ := paths(*data)
	pin, err := tlsutil.PinFromFile(cert)
	if err != nil {
		return err
	}
	fmt.Println(pin)
	return nil
}
