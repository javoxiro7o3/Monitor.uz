package alert

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"monitor.uz/internal/store"
)

// Event is published to panels when an alert changes.
type Event struct {
	Kind  string      `json:"kind"` // opened | updated | resolved
	Alert store.Alert `json:"alert"`
}

// Engine keeps active alerts in sync with conditions. Grouping: one alert
// per key (rule+agent+object) while it lasts; it is notified once, then
// reminded every RepeatMin minutes, and a "resolved" message is sent when
// the condition clears.
type Engine struct {
	store   *store.Store
	notify  *Notifier
	log     *slog.Logger
	publish func(Event)
	names   func(agentID int64) string

	mu          sync.Mutex
	active      map[string]*store.Alert
	pending     map[string]time.Time // condition first seen (for "for" rules)
	seenRunning map[string]bool      // containers seen running ("agent:name")
}

// NewEngine loads open alerts from the store.
func NewEngine(ctx context.Context, st *store.Store, n *Notifier, log *slog.Logger, publish func(Event), names func(int64) string) (*Engine, error) {
	e := &Engine{store: st, notify: n, log: log, publish: publish, names: names,
		active: map[string]*store.Alert{}, pending: map[string]time.Time{}, seenRunning: map[string]bool{}}
	open, err := st.OpenAlerts(ctx)
	if err != nil {
		return nil, err
	}
	for i := range open {
		a := open[i]
		e.active[a.Key] = &a
	}
	return e, nil
}

// Active returns a copy of active alerts.
func (e *Engine) Active() []store.Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]store.Alert, 0, len(e.active))
	for _, a := range e.active {
		out = append(out, *a)
	}
	return out
}

// Health returns the worst active severity per agent ("critical"/"warning").
func (e *Engine) Health() map[int64]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := map[int64]string{}
	for _, a := range e.active {
		if a.Rule == "offline" {
			continue // shown by the connection status
		}
		if h[a.AgentID] != Critical {
			h[a.AgentID] = a.Severity
		}
	}
	return h
}

// Silence suppresses notifications of an alert until the given time.
func (e *Engine) Silence(ctx context.Context, id int64, until time.Time) (*store.Alert, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.active {
		if a.ID == id {
			a.SilencedUntil = until.UnixMilli()
			if err := e.store.UpdateAlert(ctx, a); err != nil {
				e.log.Error("silence failed", "err", err)
			}
			cp := *a
			e.publish(Event{"updated", cp})
			return &cp, true
		}
	}
	return nil, false
}

// Tick evaluates rules once.
func (e *Engine) Tick(ctx context.Context, now time.Time, rules Rules, ov Overrides, agents []AgentState) {
	e.mu.Lock()
	defer e.mu.Unlock()

	offline := map[int64]bool{}
	present := map[int64]bool{}
	for _, a := range agents {
		present[a.ID] = true
		if a.Status == "offline" {
			offline[a.ID] = true
		}
		if a.Docker != nil {
			for _, c := range a.Docker.Containers {
				if c.State == "running" {
					e.seenRunning[strconv.FormatInt(a.ID, 10)+":"+c.Name] = true
				}
			}
		}
	}
	conds := Evaluate(rules, ov, agents, e.seenRunning)
	current := map[string]Condition{}
	for _, c := range conds {
		if c.For > 0 {
			first, ok := e.pending[c.Key]
			if !ok {
				e.pending[c.Key] = now
				continue
			}
			if now.Sub(first) < c.For {
				continue
			}
		}
		current[c.Key] = c
	}
	// Drop pending "for" conditions that cleared.
	inConds := map[string]bool{}
	for _, c := range conds {
		inConds[c.Key] = true
	}
	for k := range e.pending {
		if !inConds[k] {
			delete(e.pending, k)
		}
	}

	nowMs := now.UnixMilli()
	for key, c := range current {
		a, ok := e.active[key]
		if !ok {
			a = &store.Alert{Key: key, Rule: c.Rule, Severity: c.Severity, AgentID: c.AgentID, Object: c.Object,
				Title: c.Title, Detail: c.Detail, StartedAt: nowMs}
			if c.For > 0 {
				a.StartedAt = e.pending[key].UnixMilli()
			}
			if err := e.store.InsertAlert(ctx, a); err != nil {
				e.log.Error("storing alert failed", "err", err)
				continue
			}
			e.active[key] = a
			e.log.Warn("alert opened", "title", a.Title, "severity", a.Severity)
			e.send(a, "firing", now)
			e.store.UpdateAlert(ctx, a)
			e.publish(Event{"opened", *a})
			continue
		}
		if a.Severity != c.Severity || a.Title != c.Title {
			escalated := a.Severity == Warning && c.Severity == Critical
			a.Severity, a.Title, a.Detail = c.Severity, c.Title, c.Detail
			if escalated {
				e.send(a, "firing", now)
			}
			e.store.UpdateAlert(ctx, a)
			e.publish(Event{"updated", *a})
			continue
		}
		if rules.RepeatMin > 0 && a.NotifiedAt > 0 && now.Sub(time.UnixMilli(a.NotifiedAt)) >= time.Duration(rules.RepeatMin)*time.Minute {
			e.send(a, "reminder", now)
			e.store.UpdateAlert(ctx, a)
		}
	}
	for key, a := range e.active {
		if _, ok := current[key]; ok {
			continue
		}
		// While an agent is offline its other alerts cannot be checked:
		// keep them open instead of reporting a false recovery.
		if offline[a.AgentID] && a.Rule != "offline" && present[a.AgentID] {
			continue
		}
		a.ResolvedAt = nowMs
		if err := e.store.UpdateAlert(ctx, a); err != nil {
			e.log.Error("resolving alert failed", "err", err)
		}
		delete(e.active, key)
		e.log.Info("alert resolved", "title", a.Title)
		e.send(a, "resolved", now)
		e.publish(Event{"resolved", *a})
	}
}

func (e *Engine) send(a *store.Alert, kind string, now time.Time) {
	if a.SilencedUntil > now.UnixMilli() {
		return
	}
	if kind != "resolved" {
		a.NotifiedAt = now.UnixMilli()
	}
	if e.notify != nil {
		e.notify.Enqueue(Message{Kind: kind, Alert: *a, Server: e.names(a.AgentID)})
	}
}

// Describe is a short human text for logs/tests.
func Describe(a store.Alert) string {
	return strings.TrimSpace(a.Severity + " " + a.Title)
}
