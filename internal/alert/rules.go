// Package alert evaluates alert rules against the hub's live state and
// delivers notifications to a Slack-compatible webhook (Mattermost).
package alert

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"monitor.uz/internal/model"
)

// Severity values.
const (
	Warning  = "warning"
	Critical = "critical"
)

// Rules are the editable thresholds (defaults from the specification).
type Rules struct {
	OfflineEnabled bool `json:"offline_enabled"`

	CPUEnabled bool    `json:"cpu_enabled"`
	CPUPct     float64 `json:"cpu_pct"`
	CPUForMin  int     `json:"cpu_for_min"`

	RAMEnabled bool    `json:"ram_enabled"`
	RAMPct     float64 `json:"ram_pct"`
	RAMForMin  int     `json:"ram_for_min"`

	DiskEnabled bool    `json:"disk_enabled"`
	DiskWarn    float64 `json:"disk_warn"`
	DiskCrit    float64 `json:"disk_crit"`

	RAIDEnabled     bool    `json:"raid_enabled"`
	PhysEnabled     bool    `json:"phys_enabled"`
	DiskTempEnabled bool    `json:"disk_temp_enabled"`
	DiskTemp        float64 `json:"disk_temp"`

	ContainerEnabled bool `json:"container_enabled"`
	VMEnabled        bool `json:"vm_enabled"`

	// RepeatMin re-sends a still-active alert every N minutes (0 = never).
	RepeatMin int `json:"repeat_min"`
}

// DefaultRules returns the agreed initial rules.
func DefaultRules() Rules {
	return Rules{
		OfflineEnabled: true,
		CPUEnabled:     true, CPUPct: 90, CPUForMin: 5,
		RAMEnabled: true, RAMPct: 90, RAMForMin: 5,
		DiskEnabled: true, DiskWarn: 85, DiskCrit: 95,
		RAIDEnabled: true, PhysEnabled: true,
		DiskTempEnabled: true, DiskTemp: 55,
		ContainerEnabled: true, VMEnabled: true,
		RepeatMin: 60,
	}
}

// Validate checks ranges.
func (r Rules) Validate() error {
	in := func(v, lo, hi float64) bool { return v >= lo && v <= hi }
	switch {
	case !in(r.CPUPct, 1, 100), !in(r.RAMPct, 1, 100), !in(r.DiskWarn, 1, 100), !in(r.DiskCrit, 1, 100):
		return fmt.Errorf("foiz qiymatlari 1..100 oralig'ida bo'lishi kerak")
	case r.DiskWarn > r.DiskCrit:
		return fmt.Errorf("disk ogohlantirish chegarasi xavfli chegaradan katta bo'lmasligi kerak")
	case r.CPUForMin < 0 || r.RAMForMin < 0 || r.CPUForMin > 1440 || r.RAMForMin > 1440:
		return fmt.Errorf("davomiylik 0..1440 daqiqa")
	case !in(r.DiskTemp, 20, 100):
		return fmt.Errorf("disk harorati chegarasi 20..100 °C")
	case r.RepeatMin < 0 || r.RepeatMin > 10080:
		return fmt.Errorf("qayta eslatish 0..10080 daqiqa")
	}
	return nil
}

// AgentState is what the rules see of one agent.
type AgentState struct {
	ID      int64
	Name    string
	Mode    string
	Status  string // online | offline | new | revoked
	Latest  *model.Sample
	VMs     *model.VMReport
	Storage *model.StorageReport
	Docker  *model.DockerReport
}

// Overrides are admin choices of what is monitored. Keys: "agent:vmid"
// and "agent:container-name". Missing key = default.
type Overrides struct {
	VMs        map[string]bool `json:"vms"`
	Containers map[string]bool `json:"containers"`
}

// VMMonitored: default is "monitored if the VM starts on boot".
func (o Overrides) VMMonitored(agentID int64, v model.VM) bool {
	if m, ok := o.VMs[strconv.FormatInt(agentID, 10)+":"+strconv.Itoa(v.VMID)]; ok {
		return m
	}
	return v.OnBoot && !v.Template
}

// ContainerMonitored: default is monitored.
func (o Overrides) ContainerMonitored(agentID int64, name string) bool {
	if m, ok := o.Containers[strconv.FormatInt(agentID, 10)+":"+name]; ok {
		return m
	}
	return true
}

// Condition is a currently-true rule violation.
type Condition struct {
	Key      string
	Rule     string
	Severity string
	AgentID  int64
	Object   string
	Title    string
	Detail   string
	For      time.Duration // must hold this long before firing
}

// Evaluate returns all violated conditions. seenRunning holds containers
// ("agent:name") observed running before; only those raise "stopped".
// Agents that are offline are reported once (offline rule) and their
// other conditions are carried over by the engine, not re-evaluated.
func Evaluate(r Rules, ov Overrides, agents []AgentState, seenRunning map[string]bool) []Condition {
	var out []Condition
	add := func(c Condition) {
		c.Key = c.Rule + "|" + strconv.FormatInt(c.AgentID, 10) + "|" + c.Object
		out = append(out, c)
	}
	for _, a := range agents {
		if a.Status == "revoked" || a.Status == "new" {
			continue
		}
		if a.Status == "offline" {
			if r.OfflineEnabled {
				add(Condition{Rule: "offline", Severity: Critical, AgentID: a.ID,
					Title:  fmt.Sprintf("%s: agent javob bermayapti", a.Name),
					Detail: "Agentdan 30 soniyadan beri ma'lumot kelmayapti: server o'chgan, tarmoq uzilgan yoki agent to'xtagan."})
			}
			continue
		}
		if s := a.Latest; s != nil {
			if r.CPUEnabled && s.CPU != nil && s.CPU.Usage > r.CPUPct {
				add(Condition{Rule: "cpu", Severity: Warning, AgentID: a.ID, For: time.Duration(r.CPUForMin) * time.Minute,
					Title:  fmt.Sprintf("%s: CPU %.0f%%", a.Name, s.CPU.Usage),
					Detail: fmt.Sprintf("CPU yuklamasi %.0f%% dan yuqori (%d daqiqadan beri)", r.CPUPct, r.CPUForMin)})
			}
			if r.RAMEnabled && s.Mem != nil && s.Mem.UsedPct > r.RAMPct {
				add(Condition{Rule: "ram", Severity: Warning, AgentID: a.ID, For: time.Duration(r.RAMForMin) * time.Minute,
					Title:  fmt.Sprintf("%s: RAM %.0f%%", a.Name, s.Mem.UsedPct),
					Detail: fmt.Sprintf("Xotira bandligi %.0f%% dan yuqori (%d daqiqadan beri)", r.RAMPct, r.RAMForMin)})
			}
			if r.DiskEnabled {
				for _, f := range s.FS {
					sev := ""
					switch {
					case f.UsedPct >= r.DiskCrit:
						sev = Critical
					case f.UsedPct >= r.DiskWarn:
						sev = Warning
					}
					if sev != "" {
						add(Condition{Rule: "disk_usage", Severity: sev, AgentID: a.ID, Object: f.Mount,
							Title:  fmt.Sprintf("%s: disk bandligi %.0f%% (%s)", a.Name, f.UsedPct, f.Mount),
							Detail: fmt.Sprintf("%s (%s) to'lib bormoqda: chegaralar %.0f%% / %.0f%%", f.Mount, f.Device, r.DiskWarn, r.DiskCrit)})
					}
				}
			}
		}
		if st := a.Storage; st != nil {
			if r.RAIDEnabled {
				for _, c := range st.Controllers {
					if !strings.EqualFold(c.Status, "OK") && c.Status != "unknown" {
						add(Condition{Rule: "controller", Severity: Critical, AgentID: a.ID, Object: c.Name,
							Title: fmt.Sprintf("%s: RAID kontroller holati %s", a.Name, c.Status), Detail: c.Name})
					}
					if c.Battery != "" && !strings.EqualFold(c.Battery, "OK") {
						add(Condition{Rule: "controller_battery", Severity: Warning, AgentID: a.ID, Object: c.Name,
							Title: fmt.Sprintf("%s: kontroller batareyasi %s", a.Name, c.Battery), Detail: c.Name})
					}
				}
				for _, ld := range st.Logical {
					switch ld.State {
					case "degraded", "rebuilding", "failed":
						title := map[string]string{"degraded": "RAID degraded", "rebuilding": "RAID qayta tiklanmoqda", "failed": "RAID ishdan chiqdi"}[ld.State]
						detail := fmt.Sprintf("%s, mantiqiy disk %s (RAID %s): %s", ld.Controller, ld.ID, ld.RAID, ld.Status)
						if ld.State == "rebuilding" && ld.Progress > 0 {
							detail += fmt.Sprintf(", %.0f%%", ld.Progress)
						}
						add(Condition{Rule: "raid", Severity: Critical, AgentID: a.ID, Object: ld.Controller + "/" + ld.ID,
							Title: fmt.Sprintf("%s: %s (%s %s)", a.Name, title, ld.Controller, ld.ID), Detail: detail})
					}
				}
			}
			for _, p := range st.Physical {
				if r.PhysEnabled && (p.State == "failed" || p.State == "warning") {
					add(Condition{Rule: "phys_disk", Severity: Critical, AgentID: a.ID, Object: p.ID,
						Title:  fmt.Sprintf("%s: disk %s nosoz", a.Name, diskName(p)),
						Detail: fmt.Sprintf("%s %s, seriya %s: %s", p.Model, p.ID, p.Serial, p.Status)})
				}
				if r.DiskTempEnabled && p.Temp > r.DiskTemp {
					add(Condition{Rule: "disk_temp", Severity: Warning, AgentID: a.ID, Object: p.ID,
						Title:  fmt.Sprintf("%s: disk %s harorati %.0f °C", a.Name, diskName(p), p.Temp),
						Detail: fmt.Sprintf("Chegara %.0f °C", r.DiskTemp)})
				}
			}
		}
		if vr := a.VMs; vr != nil && r.VMEnabled {
			for _, v := range vr.VMs {
				if v.Status == "stopped" && ov.VMMonitored(a.ID, v) {
					add(Condition{Rule: "vm_stopped", Severity: Critical, AgentID: a.ID, Object: strconv.Itoa(v.VMID),
						Title:  fmt.Sprintf("%s: %s %d (%s) to'xtagan", a.Name, vmKind(v.Type), v.VMID, v.Name),
						Detail: "Kuzatiladigan VM/CT ishlamayapti"})
				}
			}
		}
		if dr := a.Docker; dr != nil && r.ContainerEnabled {
			for _, c := range dr.Containers {
				k := strconv.FormatInt(a.ID, 10) + ":" + c.Name
				if (c.State == "exited" || c.State == "dead") && seenRunning[k] && ov.ContainerMonitored(a.ID, c.Name) {
					add(Condition{Rule: "container_stopped", Severity: Critical, AgentID: a.ID, Object: c.Name,
						Title:  fmt.Sprintf("%s: konteyner %s to'xtadi", a.Name, c.Name),
						Detail: fmt.Sprintf("%s, chiqish kodi %d: %s", c.Image, c.ExitCode, c.Status)})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func diskName(p model.PhysDisk) string {
	if p.Bay != "" {
		return p.ID + " (bay " + p.Bay + ")"
	}
	return p.ID
}

func vmKind(t string) string {
	if t == "lxc" {
		return "CT"
	}
	return "VM"
}
