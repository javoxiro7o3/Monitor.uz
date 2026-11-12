package alert

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"monitor.uz/internal/model"
	"monitor.uz/internal/store"
)

type recv struct {
	mu   sync.Mutex
	msgs []map[string]any
}

func (r *recv) handler(w http.ResponseWriter, req *http.Request) {
	var m map[string]any
	json.NewDecoder(req.Body).Decode(&m)
	r.mu.Lock()
	r.msgs = append(r.msgs, m)
	r.mu.Unlock()
}

func (r *recv) texts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.msgs {
		out = append(out, m["text"].(string))
	}
	return out
}

func setup(t *testing.T) (*Engine, *recv, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rc := &recv{}
	srv := httptest.NewServer(http.HandlerFunc(rc.handler))
	t.Cleanup(srv.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	n := NewNotifier(log)
	n.Configure(WebhookConfig{Enabled: true, URL: srv.URL + "/hooks/secret"})
	go n.Run(ctx)
	e, err := NewEngine(ctx, st, n, log, func(Event) {}, func(id int64) string { return "srv-01" })
	if err != nil {
		t.Fatal(err)
	}
	return e, rc, ctx
}

func waitMsgs(t *testing.T, rc *recv, n int) []string {
	t.Helper()
	for i := 0; i < 100; i++ {
		if got := rc.texts(); len(got) >= n {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected %d messages, got %v", n, rc.texts())
	return nil
}

func TestOfflineAlertAndRecovery(t *testing.T) {
	e, rc, ctx := setup(t)
	r := DefaultRules()
	now := time.Now()
	agents := []AgentState{{ID: 1, Name: "srv-01", Status: "offline"}}
	e.Tick(ctx, now, r, Overrides{}, agents)
	got := waitMsgs(t, rc, 1)
	if !strings.Contains(got[0], "XAVFLI") || !strings.Contains(got[0], "agent javob bermayapti") {
		t.Fatalf("firing text: %q", got[0])
	}
	// Same condition again: no duplicate notification.
	e.Tick(ctx, now.Add(5*time.Second), r, Overrides{}, agents)
	agents[0].Status = "online"
	e.Tick(ctx, now.Add(10*time.Second), r, Overrides{}, agents)
	got = waitMsgs(t, rc, 2)
	if len(got) != 2 || !strings.Contains(got[1], "TIKLANDI") {
		t.Fatalf("messages: %v", got)
	}
	if len(e.Active()) != 0 {
		t.Fatal("alert still active")
	}
}

func TestCPUNeedsDuration(t *testing.T) {
	e, _, ctx := setup(t)
	r := DefaultRules()
	busy := []AgentState{{ID: 1, Name: "srv", Status: "online", Latest: &model.Sample{CPU: &model.CPU{Usage: 95}}}}
	t0 := time.Now()
	e.Tick(ctx, t0, r, Overrides{}, busy)
	e.Tick(ctx, t0.Add(4*time.Minute), r, Overrides{}, busy)
	if len(e.Active()) != 0 {
		t.Fatal("fired before 5 minutes")
	}
	e.Tick(ctx, t0.Add(5*time.Minute+time.Second), r, Overrides{}, busy)
	if a := e.Active(); len(a) != 1 || a[0].Rule != "cpu" || a[0].StartedAt != t0.UnixMilli() {
		t.Fatalf("active: %+v", a)
	}
	// A dip resets the timer.
	e2, _, _ := setup(t)
	e2.Tick(ctx, t0, r, Overrides{}, busy)
	e2.Tick(ctx, t0.Add(3*time.Minute), r, Overrides{}, []AgentState{{ID: 1, Name: "srv", Status: "online", Latest: &model.Sample{CPU: &model.CPU{Usage: 10}}}})
	e2.Tick(ctx, t0.Add(6*time.Minute), r, Overrides{}, busy)
	if len(e2.Active()) != 0 {
		t.Fatal("timer not reset by dip")
	}
}

func TestStorageAndOfflineKeepsAlerts(t *testing.T) {
	e, _, ctx := setup(t)
	r := DefaultRules()
	st := &model.StorageReport{
		Logical:  []model.LogicalDrv{{Controller: "P408i", ID: "1", RAID: "5", State: "degraded", Status: "Interim Recovery Mode"}},
		Physical: []model.PhysDisk{{ID: "1I:1:5", Bay: "5", State: "failed", Status: "Failed"}, {ID: "1I:1:1", State: "ok", Temp: 60}},
	}
	a := []AgentState{{ID: 3, Name: "srv-03", Status: "online", Storage: st}}
	now := time.Now()
	e.Tick(ctx, now, r, Overrides{}, a)
	rules := map[string]bool{}
	for _, x := range e.Active() {
		rules[x.Rule] = true
	}
	if !rules["raid"] || !rules["phys_disk"] || !rules["disk_temp"] {
		t.Fatalf("rules: %v", rules)
	}
	if e.Health()[3] != Critical {
		t.Fatal("health not critical")
	}
	// Agent goes offline: RAID alert must stay open (cannot be re-checked).
	a[0].Status = "offline"
	e.Tick(ctx, now.Add(time.Minute), r, Overrides{}, a)
	rules = map[string]bool{}
	for _, x := range e.Active() {
		rules[x.Rule] = true
	}
	if !rules["raid"] || !rules["offline"] {
		t.Fatalf("after offline: %v", rules)
	}
}

func TestVMAndContainerRules(t *testing.T) {
	e, _, ctx := setup(t)
	r := DefaultRules()
	vms := &model.VMReport{VMs: []model.VM{
		{VMID: 101, Name: "web", Status: "stopped", OnBoot: true},
		{VMID: 102, Name: "test", Status: "stopped", OnBoot: false},
		{VMID: 103, Name: "db", Status: "stopped", OnBoot: true},
	}}
	ov := Overrides{VMs: map[string]bool{"1:103": false}}
	dock := &model.DockerReport{Containers: []model.Container{{Name: "api", State: "running"}, {Name: "job", State: "exited"}}}
	a := []AgentState{{ID: 1, Name: "srv", Status: "online", VMs: vms}, {ID: 2, Name: "vm-app", Status: "online", Docker: dock}}
	now := time.Now()
	e.Tick(ctx, now, r, ov, a)
	var objs []string
	for _, x := range e.Active() {
		objs = append(objs, x.Rule+":"+x.Object)
	}
	if len(objs) != 1 || objs[0] != "vm_stopped:101" {
		t.Fatalf("expected only VM 101 (onboot, not excluded); job never ran: %v", objs)
	}
	// "api" stops after having been seen running.
	dock.Containers[0].State = "exited"
	e.Tick(ctx, now.Add(5*time.Second), r, ov, a)
	found := false
	for _, x := range e.Active() {
		if x.Rule == "container_stopped" && x.Object == "api" {
			found = true
		}
	}
	if !found {
		t.Fatal("container stop not detected")
	}
}

func TestPayloadAndRedaction(t *testing.T) {
	p := Payload(WebhookConfig{}, Message{Kind: "firing", Server: "srv-03", Alert: store.Alert{Severity: Critical, Title: "RAID degraded", Detail: "x", StartedAt: time.Now().UnixMilli()}})
	b, _ := json.Marshal(p)
	s := string(b)
	if !strings.Contains(s, `"username":"Monitor.uz"`) || !strings.Contains(s, `"color":"#FF6B6B"`) || !strings.Contains(s, "srv-03") {
		t.Fatal(s)
	}
	err := postWith(context.Background(), WebhookConfig{URL: "http://127.0.0.1:1/hooks/abcsecret"}, Message{Kind: "test"})
	if err == nil || strings.Contains(err.Error(), "abcsecret") {
		t.Fatalf("secret leaked or no error: %v", err)
	}
}

func TestRulesValidate(t *testing.T) {
	r := DefaultRules()
	if r.Validate() != nil {
		t.Fatal("defaults invalid")
	}
	r.DiskWarn = 99
	if r.Validate() == nil {
		t.Fatal("warn > crit accepted")
	}
}
