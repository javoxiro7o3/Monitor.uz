// Package model holds the wire types shared by the agent and the hub.
package model

import (
	"strconv"
	"strings"
)

// Info describes the machine an agent runs on. Sent on enrollment and
// periodically with ingest batches.
type Info struct {
	Hostname string `json:"hostname"`
	Mode     string `json:"mode"`
	Version  string `json:"version"`
	Kernel   string `json:"kernel,omitempty"`
	OS       string `json:"os,omitempty"`
	CPUModel string `json:"cpu_model,omitempty"`
	CPUCores int    `json:"cpu_cores,omitempty"`
	MemTotal uint64 `json:"mem_total,omitempty"`
	BootTime int64  `json:"boot_time,omitempty"` // unix seconds
	Arch     string `json:"arch,omitempty"`
	// Guest interfaces with MACs, used to link a guest agent to its
	// Proxmox VM/CT.
	Ifaces []IfaceAddr `json:"ifaces,omitempty"`
	// Capabilities found on the machine (ssacli, smartctl, ipmitool, docker).
	Tools []string `json:"tools,omitempty"`
}

// Sample is one collection tick. Nil/empty sections mean "not available"
// for that tick; Unavailable names the sections that failed to collect.
type Sample struct {
	TS          int64      `json:"ts"` // unix milliseconds (wall clock)
	Uptime      float64    `json:"uptime,omitempty"`
	CPU         *CPU       `json:"cpu,omitempty"`
	Load        *Load      `json:"load,omitempty"`
	Mem         *Mem       `json:"mem,omitempty"`
	Net         []NetIface `json:"net,omitempty"`
	Disks       []DiskIO   `json:"disks,omitempty"`
	FS          []FS       `json:"fs,omitempty"`
	PSI         *PSI       `json:"psi,omitempty"`
	Unavailable []string   `json:"na,omitempty"`
}

// CPU usage in percent over the last interval.
type CPU struct {
	Usage  float64   `json:"usage"`
	User   float64   `json:"user"`
	System float64   `json:"system"`
	IOWait float64   `json:"iowait"`
	Steal  float64   `json:"steal"`
	IRQ    float64   `json:"irq"`
	Cores  []float64 `json:"cores,omitempty"`
}

// Load is /proc/loadavg.
type Load struct {
	L1      float64 `json:"l1"`
	L5      float64 `json:"l5"`
	L15     float64 `json:"l15"`
	Running int     `json:"running"`
	Total   int     `json:"total"`
}

// Mem values are bytes; percentages are 0..100.
type Mem struct {
	Total     uint64  `json:"total"`
	Available uint64  `json:"available"`
	Used      uint64  `json:"used"` // Total - Available
	Buffers   uint64  `json:"buffers"`
	Cached    uint64  `json:"cached"`
	UsedPct   float64 `json:"used_pct"`
	SwapTotal uint64  `json:"swap_total"`
	SwapUsed  uint64  `json:"swap_used"`
	SwapPct   float64 `json:"swap_pct"`
	// ZFS ARC (only when the zfs module is loaded). ARC is not counted in
	// MemAvailable, so it is reported separately.
	ARCSize uint64 `json:"arc_size,omitempty"`
	ARCMax  uint64 `json:"arc_max,omitempty"`
}

// NetIface rates are per second over the last interval.
type NetIface struct {
	Name  string  `json:"name"`
	Kind  string  `json:"kind"` // phys, bridge, bond, vlan, other
	RxBps float64 `json:"rx_bps"`
	TxBps float64 `json:"tx_bps"`
	RxPps float64 `json:"rx_pps"`
	TxPps float64 `json:"tx_pps"`
	Errs  float64 `json:"errs"`  // rx+tx errors per second
	Drops float64 `json:"drops"` // rx+tx drops per second
}

// DiskIO rates are per second over the last interval.
type DiskIO struct {
	Name      string  `json:"name"`
	ReadBps   float64 `json:"read_bps"`
	WriteBps  float64 `json:"write_bps"`
	ReadIOPS  float64 `json:"read_iops"`
	WriteIOPS float64 `json:"write_iops"`
	UtilPct   float64 `json:"util"`     // time the device was busy, 0..100
	AwaitMs   float64 `json:"await_ms"` // average time per completed I/O
}

// FS is filesystem usage from statvfs.
type FS struct {
	Mount   string  `json:"mount"`
	Device  string  `json:"device"`
	Type    string  `json:"type"`
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Avail   uint64  `json:"avail"`
	UsedPct float64 `json:"used_pct"`
}

// PSI is the share of wall time (percent) that tasks were stalled during
// the last interval, computed from the "total" counters.
type PSI struct {
	CPUSome float64 `json:"cpu_some"`
	CPUFull float64 `json:"cpu_full"`
	MemSome float64 `json:"mem_some"`
	MemFull float64 `json:"mem_full"`
	IOSome  float64 `json:"io_some"`
	IOFull  float64 `json:"io_full"`
}

// IngestRequest is what an agent POSTs to the hub.
type IngestRequest struct {
	Info    *Info    `json:"info,omitempty"`
	Samples []Sample `json:"samples"`
	Reports *Reports `json:"reports,omitempty"`
}

// IngestResponse may ask the agent to update itself.
type IngestResponse struct {
	Update *UpdateOrder `json:"update,omitempty"`
}

// UpdateOrder tells an agent to download and switch to another binary
// version (an admin approved it in the panel).
type UpdateOrder struct {
	Version string `json:"version"`
	URL     string `json:"url"` // path on the hub, e.g. /dl/agent/0.2.0/monitor-agent-linux-amd64
	SHA256  string `json:"sha256"`
}

// EnrollRequest exchanges a one-time install token for a permanent agent token.
type EnrollRequest struct {
	Token string `json:"token"`
	Info  Info   `json:"info"`
}

// EnrollResponse is returned on successful enrollment.
type EnrollResponse struct {
	AgentID int64  `json:"agent_id"`
	Token   string `json:"token"`
}

// NoRollupPrefix marks series kept only at raw resolution (not aggregated
// into the 10 s history) to keep the weekly store small.
const NoRollupPrefix = "cpu.core["

// Flatten turns a sample into named numeric series for storage and charts.
// Key format: "group.field" or "group[name].field".
func (s *Sample) Flatten() map[string]float64 {
	m := make(map[string]float64, 128)
	if c := s.CPU; c != nil {
		m["cpu.usage"] = c.Usage
		m["cpu.user"] = c.User
		m["cpu.system"] = c.System
		m["cpu.iowait"] = c.IOWait
		m["cpu.steal"] = c.Steal
		for i, v := range c.Cores {
			m["cpu.core["+strconv.Itoa(i)+"]"] = v
		}
	}
	if l := s.Load; l != nil {
		m["load.1"] = l.L1
		m["load.5"] = l.L5
		m["load.15"] = l.L15
	}
	if x := s.Mem; x != nil {
		m["mem.used_pct"] = x.UsedPct
		m["mem.used"] = float64(x.Used)
		m["mem.swap_pct"] = x.SwapPct
		if x.ARCMax > 0 {
			m["mem.arc"] = float64(x.ARCSize)
		}
	}
	if len(s.Net) > 0 {
		var rx, tx float64
		for _, n := range s.Net {
			p := "net[" + n.Name + "]."
			m[p+"rx_bps"] = n.RxBps
			m[p+"tx_bps"] = n.TxBps
			m[p+"rx_pps"] = n.RxPps
			m[p+"tx_pps"] = n.TxPps
			m[p+"errs"] = n.Errs
			m[p+"drops"] = n.Drops
			if n.Kind == "phys" {
				rx += n.RxBps
				tx += n.TxBps
			}
		}
		m["net.rx_bps"] = rx
		m["net.tx_bps"] = tx
	}
	if len(s.Disks) > 0 {
		var r, w float64
		for _, d := range s.Disks {
			p := "disk[" + d.Name + "]."
			m[p+"read_bps"] = d.ReadBps
			m[p+"write_bps"] = d.WriteBps
			m[p+"read_iops"] = d.ReadIOPS
			m[p+"write_iops"] = d.WriteIOPS
			m[p+"util"] = d.UtilPct
			m[p+"await_ms"] = d.AwaitMs
			if !strings.HasPrefix(d.Name, "md") { // md arrays would double-count member I/O
				r += d.ReadBps
				w += d.WriteBps
			}
		}
		m["disk.read_bps"] = r
		m["disk.write_bps"] = w
	}
	for _, f := range s.FS {
		m["fs["+f.Mount+"].used_pct"] = f.UsedPct
	}
	if p := s.PSI; p != nil {
		m["psi.cpu_some"] = p.CPUSome
		m["psi.mem_some"] = p.MemSome
		m["psi.mem_full"] = p.MemFull
		m["psi.io_some"] = p.IOSome
		m["psi.io_full"] = p.IOFull
	}
	return m
}
