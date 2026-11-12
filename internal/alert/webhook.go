package alert

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"monitor.uz/internal/store"
)

// WebhookConfig is the notification channel setting. URL is a secret and
// is stored encrypted by the hub.
type WebhookConfig struct {
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"`
	Insecure bool   `json:"insecure"` // skip TLS verification (internal self-signed Mattermost)
	Channel  string `json:"channel,omitempty"`
}

// Message is one notification.
type Message struct {
	Kind   string // firing | reminder | resolved | test
	Alert  store.Alert
	Server string
}

// Status describes delivery health for the panel.
type Status struct {
	Enabled     bool   `json:"enabled"`
	Configured  bool   `json:"configured"`
	Queued      int    `json:"queued"`
	Sent        int64  `json:"sent"`
	Failed      int64  `json:"failed"`
	LastOK      int64  `json:"last_ok,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	LastErrorAt int64  `json:"last_error_at,omitempty"`
}

// Notifier delivers messages with retries (5 s, 30 s, 2 min, 10 min, 30 min).
type Notifier struct {
	log   *slog.Logger
	queue chan Message

	mu     sync.Mutex
	cfg    WebhookConfig
	status Status
}

var retryDelays = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute}

// NewNotifier creates a notifier; call Run in a goroutine.
func NewNotifier(log *slog.Logger) *Notifier {
	return &Notifier{log: log, queue: make(chan Message, 1000)}
}

// Configure replaces the webhook settings.
func (n *Notifier) Configure(c WebhookConfig) {
	n.mu.Lock()
	n.cfg = c
	n.mu.Unlock()
}

// Status returns delivery statistics.
func (n *Notifier) Status() Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.status
	s.Enabled, s.Configured, s.Queued = n.cfg.Enabled, n.cfg.URL != "", len(n.queue)
	return s
}

// Enqueue adds a message; when the queue is full the message is dropped
// and counted as failed (the alert stays visible in the panel).
func (n *Notifier) Enqueue(m Message) {
	n.mu.Lock()
	enabled := n.cfg.Enabled && n.cfg.URL != ""
	n.mu.Unlock()
	if !enabled {
		return
	}
	select {
	case n.queue <- m:
	default:
		n.fail(errors.New("navbat to'lgan, xabar tashlab yuborildi"))
	}
}

// Run delivers queued messages until ctx ends.
func (n *Notifier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-n.queue:
			for attempt := 0; ; attempt++ {
				err := n.post(ctx, m)
				if err == nil {
					n.mu.Lock()
					n.status.Sent++
					n.status.LastOK = time.Now().UnixMilli()
					n.mu.Unlock()
					break
				}
				n.log.Warn("webhook delivery failed", "attempt", attempt+1, "err", err)
				if attempt >= len(retryDelays) {
					n.fail(err)
					break
				}
				n.setErr(err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(retryDelays[attempt]):
				}
			}
		}
	}
}

func (n *Notifier) setErr(err error) {
	n.mu.Lock()
	n.status.LastError = err.Error()
	n.status.LastErrorAt = time.Now().UnixMilli()
	n.mu.Unlock()
}

func (n *Notifier) fail(err error) {
	n.mu.Lock()
	n.status.Failed++
	n.mu.Unlock()
	n.setErr(err)
}

// Test sends a test message synchronously with the given config.
func (n *Notifier) Test(ctx context.Context, c WebhookConfig) error {
	return postWith(ctx, c, Message{Kind: "test", Alert: store.Alert{Title: "Sinov xabari", Detail: "Monitor.uz webhook to'g'ri sozlangan.", StartedAt: time.Now().UnixMilli()}})
}

func (n *Notifier) post(ctx context.Context, m Message) error {
	n.mu.Lock()
	c := n.cfg
	n.mu.Unlock()
	if !c.Enabled || c.URL == "" {
		return nil
	}
	return postWith(ctx, c, m)
}

// Payload builds the Slack-compatible JSON (Mattermost incoming webhook).
func Payload(c WebhookConfig, m Message) map[string]any {
	a := m.Alert
	color, head := "#F5B544", "Ogohlantirish"
	if a.Severity == Critical {
		color, head = "#FF6B6B", "Xavfli"
	}
	text := ""
	switch m.Kind {
	case "firing":
		text = fmt.Sprintf("**%s**: %s", strings.ToUpper(head), a.Title)
	case "reminder":
		text = fmt.Sprintf("**Hali ham faol** (%s): %s", humanDur(time.Since(time.UnixMilli(a.StartedAt))), a.Title)
	case "resolved":
		color = "#5BD58A"
		text = fmt.Sprintf("**TIKLANDI**: %s (davomiyligi %s)", a.Title, humanDur(time.UnixMilli(a.ResolvedAt).Sub(time.UnixMilli(a.StartedAt))))
	case "test":
		color = "#2BD9C4"
		text = "**Monitor.uz**: sinov xabari. Webhook ishlayapti."
	}
	att := map[string]any{"fallback": text, "color": color, "text": a.Detail, "footer": "Monitor.uz",
		"ts": time.UnixMilli(a.StartedAt).Unix()}
	if m.Server != "" {
		att["fields"] = []map[string]any{
			{"short": true, "title": "Server", "value": m.Server},
			{"short": true, "title": "Boshlangan", "value": time.UnixMilli(a.StartedAt).Format("02.01.2006 15:04:05")},
		}
	}
	p := map[string]any{"username": "Monitor.uz", "text": text, "attachments": []any{att}}
	if c.Channel != "" {
		p["channel"] = c.Channel
	}
	return p
}

func postWith(ctx context.Context, c WebhookConfig, m Message) error {
	if c.URL == "" {
		return errors.New("webhook manzili kiritilmagan")
	}
	body, _ := json.Marshal(Payload(c, m))
	tr := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{InsecureSkipVerify: c.Insecure, MinVersion: tls.VersionTLS12}} //nolint:gosec // admin-controlled for internal CAs
	cl := &http.Client{Timeout: 15 * time.Second, Transport: tr}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("noto'g'ri webhook manzili")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		// Never log or show the full URL: it contains the webhook secret.
		return fmt.Errorf("ulanib bo'lmadi: %s", redact(err.Error(), c.URL))
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("qabul qiluvchi %s javob berdi: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func redact(s, url string) string {
	if url == "" {
		return s
	}
	host := url
	if i := strings.Index(url, "://"); i >= 0 {
		host = url[i+3:]
	}
	if i := strings.IndexByte(host, '/'); i >= 0 {
		return strings.ReplaceAll(s, url, url[:len(url)-len(host)+i]+"/…")
	}
	return s
}

func humanDur(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		return "1 daqiqadan kam"
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if h >= 24 {
		return fmt.Sprintf("%d kun %d soat", h/24, h%24)
	}
	if h > 0 {
		return fmt.Sprintf("%d soat %d daqiqa", h, m)
	}
	return fmt.Sprintf("%d daqiqa", m)
}
