package api

import (
	"sort"
	"sync"
	"time"

	"monitor.uz/internal/model"
	"monitor.uz/internal/store"
)

// Agent status values shown in the panel.
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
	StatusNew     = "new" // enrolled, never sent data
	StatusRevoked = "revoked"
)

// AgentView is an agent as the panel sees it.
type AgentView struct {
	store.Agent
	Status string        `json:"status"`
	Latest *model.Sample `json:"latest,omitempty"`
}

type liveAgent struct {
	agent    store.Agent
	latest   *model.Sample
	lastSeen time.Time // hub receive time
	online   bool
	dirty    bool // lastSeen not yet persisted
}

// Live holds in-memory agent state: latest sample and online status.
type Live struct {
	mu      sync.RWMutex
	agents  map[int64]*liveAgent
	timeout time.Duration
}

func newLive(agents []store.Agent, timeout time.Duration) *Live {
	l := &Live{agents: map[int64]*liveAgent{}, timeout: timeout}
	for _, a := range agents {
		la := &liveAgent{agent: a}
		if a.LastSeen > 0 {
			la.lastSeen = time.UnixMilli(a.LastSeen)
		}
		l.agents[a.ID] = la
	}
	return l
}

func (la *liveAgent) view() AgentView {
	v := AgentView{Agent: la.agent, Latest: la.latest}
	v.LastSeen = la.lastSeen.UnixMilli()
	if la.lastSeen.IsZero() {
		v.LastSeen = 0
	}
	switch {
	case la.agent.Revoked:
		v.Status = StatusRevoked
	case la.online:
		v.Status = StatusOnline
	case la.lastSeen.IsZero():
		v.Status = StatusNew
	default:
		v.Status = StatusOffline
	}
	return v
}

// upsert registers or refreshes an agent record (after enrollment).
func (l *Live) upsert(a store.Agent) AgentView {
	l.mu.Lock()
	defer l.mu.Unlock()
	la, ok := l.agents[a.ID]
	if !ok {
		la = &liveAgent{}
		l.agents[a.ID] = la
	}
	la.agent = a
	return la.view()
}

// seen records a delivery. It returns whether the agent just came online.
func (l *Live) seen(id int64, now time.Time, newest *model.Sample, info *model.Info) (cameOnline bool, view AgentView) {
	l.mu.Lock()
	defer l.mu.Unlock()
	la, ok := l.agents[id]
	if !ok {
		la = &liveAgent{agent: store.Agent{ID: id}}
		l.agents[id] = la
	}
	la.lastSeen, la.dirty = now, true
	if info != nil {
		la.agent.Info = *info
	}
	if newest != nil && (la.latest == nil || newest.TS >= la.latest.TS) {
		la.latest = newest
	}
	if !la.online {
		la.online = true
		cameOnline = true
	}
	return cameOnline, la.view()
}

// expire marks agents offline whose last delivery is older than timeout.
func (l *Live) expire(now time.Time) []AgentView {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []AgentView
	for _, la := range l.agents {
		if la.online && now.Sub(la.lastSeen) > l.timeout {
			la.online = false
			out = append(out, la.view())
		}
	}
	return out
}

// takeDirty returns last-seen times not yet written to the database.
func (l *Live) takeDirty() map[int64]int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[int64]int64{}
	for id, la := range l.agents {
		if la.dirty {
			out[id] = la.lastSeen.UnixMilli()
			la.dirty = false
		}
	}
	return out
}

func (l *Live) list() []AgentView {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]AgentView, 0, len(l.agents))
	for _, la := range l.agents {
		out = append(out, la.view())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (l *Live) get(id int64) (AgentView, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	la, ok := l.agents[id]
	if !ok {
		return AgentView{}, false
	}
	return la.view(), true
}

func (l *Live) setRevoked(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if la, ok := l.agents[id]; ok {
		la.agent.Revoked = true
		la.online = false
	}
}
