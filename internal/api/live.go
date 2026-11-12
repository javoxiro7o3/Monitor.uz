package api

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"monitor.uz/internal/alert"
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

// Reports are the newest slow reports of an agent.
type Reports struct {
	VMs     *model.VMReport      `json:"vms,omitempty"`
	Net     *model.HostNetReport `json:"net,omitempty"`
	Storage *model.StorageReport `json:"storage,omitempty"`
	Sensors *model.SensorReport  `json:"sensors,omitempty"`
	Procs   *model.ProcReport    `json:"procs,omitempty"`
	Docker  *model.DockerReport  `json:"docker,omitempty"`
}

// GuestLink ties a guest agent to the Proxmox VM/CT it runs in (matched
// by MAC address).
type GuestLink struct {
	HostID int64  `json:"host_id"`
	VMID   int    `json:"vmid"`
	VMName string `json:"vm_name"`
	Type   string `json:"type"`
}

// AgentView is an agent as the panel sees it.
type AgentView struct {
	store.Agent
	Status  string        `json:"status"`
	Health  string        `json:"health,omitempty"` // warning | critical (from active alerts)
	Latest  *model.Sample `json:"latest,omitempty"`
	Reports Reports       `json:"reports"`
	Link    *GuestLink    `json:"link,omitempty"`
	Desired string        `json:"desired_version,omitempty"`
}

type liveAgent struct {
	agent    store.Agent
	latest   *model.Sample
	reports  Reports
	lastSeen time.Time // hub receive time
	online   bool
	dirty    bool // lastSeen not yet persisted
	desired  string
}

// Live holds in-memory agent state.
type Live struct {
	mu      sync.RWMutex
	agents  map[int64]*liveAgent
	timeout time.Duration
	health  map[int64]string
}

func newLive(agents []store.Agent, timeout time.Duration) *Live {
	l := &Live{agents: map[int64]*liveAgent{}, timeout: timeout, health: map[int64]string{}}
	for _, a := range agents {
		la := &liveAgent{agent: a}
		if a.LastSeen > 0 {
			la.lastSeen = time.UnixMilli(a.LastSeen)
		}
		l.agents[a.ID] = la
	}
	return l
}

func (l *Live) restoreReports(saved map[int64]map[string]json.RawMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, kinds := range saved {
		la, ok := l.agents[id]
		if !ok {
			continue
		}
		for kind, raw := range kinds {
			setReport(&la.reports, kind, raw)
		}
	}
}

func setReport(r *Reports, kind string, raw json.RawMessage) {
	switch kind {
	case "vms":
		json.Unmarshal(raw, &r.VMs)
	case "net":
		json.Unmarshal(raw, &r.Net)
	case "storage":
		json.Unmarshal(raw, &r.Storage)
	case "sensors":
		json.Unmarshal(raw, &r.Sensors)
	case "procs":
		json.Unmarshal(raw, &r.Procs)
	case "docker":
		json.Unmarshal(raw, &r.Docker)
	}
}

func (la *liveAgent) status() string {
	switch {
	case la.agent.Revoked:
		return StatusRevoked
	case la.online:
		return StatusOnline
	case la.lastSeen.IsZero():
		return StatusNew
	}
	return StatusOffline
}

func (l *Live) viewLocked(la *liveAgent, links map[string]GuestLink) AgentView {
	v := AgentView{Agent: la.agent, Latest: la.latest, Reports: la.reports, Status: la.status(), Desired: la.desired}
	v.LastSeen = 0
	if !la.lastSeen.IsZero() {
		v.LastSeen = la.lastSeen.UnixMilli()
	}
	v.Health = l.health[la.agent.ID]
	if la.agent.Mode == "guest" && links != nil {
		for _, ifc := range la.agent.Info.Ifaces {
			if g, ok := links[strings.ToUpper(ifc.MAC)]; ok {
				gl := g
				v.Link = &gl
				break
			}
		}
	}
	return v
}

// macIndex maps VM NIC MACs to their VM (from host reports).
func (l *Live) macIndexLocked() map[string]GuestLink {
	idx := map[string]GuestLink{}
	for id, la := range l.agents {
		if la.reports.VMs == nil {
			continue
		}
		for _, vm := range la.reports.VMs.VMs {
			for _, n := range vm.Nets {
				if n.MAC != "" {
					idx[strings.ToUpper(n.MAC)] = GuestLink{HostID: id, VMID: vm.VMID, VMName: vm.Name, Type: vm.Type}
				}
			}
		}
	}
	return idx
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
	return l.viewLocked(la, l.macIndexLocked())
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
	return cameOnline, l.viewLocked(la, l.macIndexLocked())
}

// setReports stores newer reports and returns the kinds that changed.
func (l *Live) setReports(id int64, r *model.Reports) map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	la, ok := l.agents[id]
	if !ok || r == nil {
		return nil
	}
	ch := map[string]any{}
	if r.VMs != nil && (la.reports.VMs == nil || r.VMs.TS >= la.reports.VMs.TS) {
		la.reports.VMs, ch["vms"] = r.VMs, r.VMs
	}
	if r.Net != nil && (la.reports.Net == nil || r.Net.TS >= la.reports.Net.TS) {
		la.reports.Net, ch["net"] = r.Net, r.Net
	}
	if r.Storage != nil && (la.reports.Storage == nil || r.Storage.TS >= la.reports.Storage.TS) {
		la.reports.Storage, ch["storage"] = r.Storage, r.Storage
	}
	if r.Sensors != nil && (la.reports.Sensors == nil || r.Sensors.TS >= la.reports.Sensors.TS) {
		la.reports.Sensors, ch["sensors"] = r.Sensors, r.Sensors
	}
	if r.Procs != nil && (la.reports.Procs == nil || r.Procs.TS >= la.reports.Procs.TS) {
		la.reports.Procs, ch["procs"] = r.Procs, r.Procs
	}
	if r.Docker != nil && (la.reports.Docker == nil || r.Docker.TS >= la.reports.Docker.TS) {
		la.reports.Docker, ch["docker"] = r.Docker, r.Docker
	}
	return ch
}

// expire marks agents offline whose last delivery is older than timeout.
func (l *Live) expire(now time.Time) []AgentView {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []AgentView
	for _, la := range l.agents {
		if la.online && now.Sub(la.lastSeen) > l.timeout {
			la.online = false
			out = append(out, l.viewLocked(la, nil))
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
	idx := l.macIndexLocked()
	out := make([]AgentView, 0, len(l.agents))
	for _, la := range l.agents {
		out = append(out, l.viewLocked(la, idx))
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
	return l.viewLocked(la, l.macIndexLocked()), true
}

func (l *Live) name(id int64) string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if la, ok := l.agents[id]; ok {
		return la.agent.Name
	}
	return ""
}

func (l *Live) version(id int64) (version, arch string) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if la, ok := l.agents[id]; ok {
		return la.agent.Info.Version, la.agent.Info.Arch
	}
	return "", ""
}

func (l *Live) setRevoked(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if la, ok := l.agents[id]; ok {
		la.agent.Revoked = true
		la.online = false
	}
}

func (l *Live) remove(id int64) {
	l.mu.Lock()
	delete(l.agents, id)
	l.mu.Unlock()
}

func (l *Live) setDesired(m map[int64]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, la := range l.agents {
		la.desired = m[id]
	}
}

func (l *Live) setHealth(h map[int64]string) (changed []int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id := range l.agents {
		if l.health[id] != h[id] {
			changed = append(changed, id)
		}
	}
	l.health = h
	return changed
}

// alertStates snapshots what the alert rules need.
func (l *Live) alertStates() []alert.AgentState {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]alert.AgentState, 0, len(l.agents))
	for id, la := range l.agents {
		out = append(out, alert.AgentState{
			ID: id, Name: la.agent.Name, Mode: la.agent.Mode, Status: la.status(),
			Latest: la.latest, VMs: la.reports.VMs, Storage: la.reports.Storage, Docker: la.reports.Docker,
		})
	}
	return out
}
