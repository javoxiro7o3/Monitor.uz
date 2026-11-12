package model

import "strconv"

// Reports collected on slower cadences than the 1 s Sample. Each carries
// its own timestamp; the agent attaches the newest of each kind to the
// next ingest request.

// Reports groups the optional slow sections of an ingest request.
type Reports struct {
	VMs     *VMReport      `json:"vms,omitempty"`     // host, 5 s
	Net     *HostNetReport `json:"net,omitempty"`     // host, 60 s
	Storage *StorageReport `json:"storage,omitempty"` // host, 60 s
	Sensors *SensorReport  `json:"sensors,omitempty"` // host, 10 s
	Procs   *ProcReport    `json:"procs,omitempty"`   // guest, 5 s
	Docker  *DockerReport  `json:"docker,omitempty"`  // guest, 5 s
}

// ---------- Proxmox VM/CT (host side) ----------

// VMReport lists all VMs and CTs defined on a Proxmox host.
type VMReport struct {
	TS    int64  `json:"ts"`
	Node  string `json:"node,omitempty"`
	VMs   []VM   `json:"vms"`
	Error string `json:"error,omitempty"`
}

// VM is a QEMU VM or LXC container as seen from the host.
type VM struct {
	VMID     int     `json:"vmid"`
	Type     string  `json:"type"` // qemu | lxc
	Name     string  `json:"name"`
	Status   string  `json:"status"` // running | stopped
	Template bool    `json:"template,omitempty"`
	OnBoot   bool    `json:"onboot,omitempty"`
	OSType   string  `json:"ostype,omitempty"`
	Cores    float64 `json:"cores,omitempty"`   // vCPUs (or CT cpulimit/cores)
	MemMax   uint64  `json:"mem_max,omitempty"` // bytes (configured)
	Uptime   float64 `json:"uptime,omitempty"`  // seconds, running only
	CPU      float64 `json:"cpu"`               // % of configured cores
	MemUsed  uint64  `json:"mem_used"`          // bytes (cgroup)
	ReadBps  float64 `json:"read_bps"`
	WriteBps float64 `json:"write_bps"`
	NetRxBps float64 `json:"net_rx_bps"` // traffic received by the guest
	NetTxBps float64 `json:"net_tx_bps"` // traffic sent by the guest
	Nets     []VMNet `json:"nets,omitempty"`
	Tags     string  `json:"tags,omitempty"`
}

// VMNet is one netN line of a VM/CT config.
type VMNet struct {
	ID       string `json:"id"` // net0
	Model    string `json:"model,omitempty"`
	MAC      string `json:"mac,omitempty"` // upper-case
	Bridge   string `json:"bridge,omitempty"`
	Tag      int    `json:"tag,omitempty"`
	Firewall bool   `json:"firewall,omitempty"`
	IP       string `json:"ip,omitempty"` // CT static IP from config
	Iface    string `json:"iface,omitempty"`
}

// HostNetReport describes host interfaces and bridges.
type HostNetReport struct {
	TS      int64       `json:"ts"`
	Ifaces  []HostIface `json:"ifaces"`
	Bridges []Bridge    `json:"bridges"`
}

// HostIface is a host network interface.
type HostIface struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	MAC    string   `json:"mac,omitempty"`
	Up     bool     `json:"up"`
	Speed  int      `json:"speed,omitempty"` // Mbit/s
	Addrs  []string `json:"addrs,omitempty"`
	Master string   `json:"master,omitempty"` // bridge/bond it belongs to
}

// Bridge is a Linux bridge (vmbrN) with its ports.
type Bridge struct {
	Name      string   `json:"name"`
	Ports     []string `json:"ports"` // non-VM ports (NICs, bonds, VLANs)
	VLANAware bool     `json:"vlan_aware,omitempty"`
	Addrs     []string `json:"addrs,omitempty"`
}

// ---------- Disks / RAID ----------

// StorageReport is the physical disk and RAID state of a host.
type StorageReport struct {
	TS          int64        `json:"ts"`
	Source      string       `json:"source"` // ssacli | smartctl | none
	Controllers []Controller `json:"controllers,omitempty"`
	Logical     []LogicalDrv `json:"logical,omitempty"`
	Physical    []PhysDisk   `json:"physical,omitempty"`
	Errors      []string     `json:"errors,omitempty"`
}

// Controller is a RAID controller.
type Controller struct {
	Name    string `json:"name"`
	Slot    string `json:"slot,omitempty"`
	Serial  string `json:"serial,omitempty"`
	Status  string `json:"status"`
	Cache   string `json:"cache,omitempty"`
	Battery string `json:"battery,omitempty"`
}

// LogicalDrv is a RAID logical drive.
type LogicalDrv struct {
	Controller string   `json:"controller"`
	ID         string   `json:"id"`
	RAID       string   `json:"raid"` // "1", "5", "1+0", ...
	Size       uint64   `json:"size,omitempty"`
	Status     string   `json:"status"` // raw tool text
	State      string   `json:"state"`  // ok | degraded | rebuilding | failed | unknown
	Device     string   `json:"device,omitempty"`
	Members    []string `json:"members,omitempty"` // physical disk ids
	Spares     []string `json:"spares,omitempty"`
	Progress   float64  `json:"progress,omitempty"` // rebuild %
}

// PhysDisk is one physical disk.
type PhysDisk struct {
	ID           string  `json:"id"` // ssacli "1I:1:1" or /dev/sdX
	Controller   string  `json:"controller,omitempty"`
	Bay          string  `json:"bay,omitempty"`
	Model        string  `json:"model,omitempty"`
	Serial       string  `json:"serial,omitempty"`
	Size         uint64  `json:"size,omitempty"`
	Type         string  `json:"type,omitempty"` // HDD | SSD | NVMe
	Interface    string  `json:"interface,omitempty"`
	Temp         float64 `json:"temp,omitempty"`
	MaxTemp      float64 `json:"max_temp,omitempty"`
	Status       string  `json:"status"` // raw tool text
	State        string  `json:"state"`  // ok | failed | warning | rebuilding | spare | unknown
	SMART        string  `json:"smart,omitempty"`
	PowerOnHours int64   `json:"power_on_hours,omitempty"`
	Role         string  `json:"role,omitempty"` // data | spare | unassigned
}

// ---------- Sensors ----------

// SensorReport holds temperatures, fans and power supplies.
type SensorReport struct {
	TS     int64    `json:"ts"`
	Source string   `json:"source"` // ipmitool | hwmon | none
	Temps  []Sensor `json:"temps,omitempty"`
	Fans   []Sensor `json:"fans,omitempty"`
	PSUs   []Sensor `json:"psus,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// Sensor is one reading. Value is NaN-free; Unit is "C", "RPM", "%" or "".
type Sensor struct {
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit,omitempty"`
	Status string  `json:"status"` // ok | warning | critical | unknown (+ raw in Detail)
	Detail string  `json:"detail,omitempty"`
}

// ---------- Guest ----------

// ProcReport lists the busiest processes inside a guest.
type ProcReport struct {
	TS    int64  `json:"ts"`
	Total int    `json:"total"`
	Top   []Proc `json:"top"`
}

// Proc is one process.
type Proc struct {
	PID  int     `json:"pid"`
	Name string  `json:"name"`
	User string  `json:"user,omitempty"`
	CPU  float64 `json:"cpu"` // % of one core
	RSS  uint64  `json:"rss"`
}

// DockerReport lists containers of a guest.
type DockerReport struct {
	TS         int64       `json:"ts"`
	Containers []Container `json:"containers"`
	Error      string      `json:"error,omitempty"`
}

// Container is one Docker container.
type Container struct {
	ID       string  `json:"id"` // 12-char short id
	Name     string  `json:"name"`
	Image    string  `json:"image"`
	State    string  `json:"state"` // running | exited | restarting | paused | dead | created
	Status   string  `json:"status"`
	Health   string  `json:"health,omitempty"`
	Restarts int     `json:"restarts"`
	ExitCode int     `json:"exit_code,omitempty"`
	CPU      float64 `json:"cpu"` // % of one core
	MemUsed  uint64  `json:"mem_used"`
	MemLimit uint64  `json:"mem_limit,omitempty"`
	NetRxBps float64 `json:"net_rx_bps"`
	NetTxBps float64 `json:"net_tx_bps"`
	ReadBps  float64 `json:"read_bps"`
	WriteBps float64 `json:"write_bps"`
	Started  int64   `json:"started,omitempty"` // unix s
}

// IfaceAddr is a guest interface; used to match guests to Proxmox VMs by MAC.
type IfaceAddr struct {
	Name  string   `json:"name"`
	MAC   string   `json:"mac"`
	Addrs []string `json:"addrs,omitempty"`
}

// FlattenVMs returns per-VM series for history.
func (r *VMReport) Flatten() map[string]float64 {
	m := map[string]float64{}
	for _, v := range r.VMs {
		if v.Template {
			continue
		}
		p := "vm[" + strconv.Itoa(v.VMID) + "]."
		run := 0.0
		if v.Status == "running" {
			run = 1
		}
		m[p+"up"] = run
		if run == 0 {
			continue
		}
		m[p+"cpu"] = v.CPU
		m[p+"mem"] = float64(v.MemUsed)
		m[p+"read_bps"] = v.ReadBps
		m[p+"write_bps"] = v.WriteBps
		m[p+"net_rx_bps"] = v.NetRxBps
		m[p+"net_tx_bps"] = v.NetTxBps
	}
	return m
}

// Flatten returns per-container series for history.
func (r *DockerReport) Flatten() map[string]float64 {
	m := map[string]float64{}
	for _, c := range r.Containers {
		if c.State != "running" {
			continue
		}
		p := "ct[" + c.Name + "]."
		m[p+"cpu"] = c.CPU
		m[p+"mem"] = float64(c.MemUsed)
		m[p+"net_rx_bps"] = c.NetRxBps
		m[p+"net_tx_bps"] = c.NetTxBps
	}
	return m
}

// Flatten returns sensor/disk temperature series.
func (r *SensorReport) Flatten() map[string]float64 {
	m := map[string]float64{}
	for _, s := range r.Temps {
		m["temp["+s.Name+"]"] = s.Value
	}
	for _, s := range r.Fans {
		m["fan["+s.Name+"]"] = s.Value
	}
	return m
}
