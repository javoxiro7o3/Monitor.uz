package collector

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"monitor.uz/internal/model"
)

// Sensors reads temperatures, fans and PSUs. In-band IPMI (ipmitool via
// /dev/ipmi0) is used when available; otherwise kernel hwmon (temperatures
// and fans only). iLO is never contacted over the network.
type Sensors struct {
	Run      Runner
	SysRoot  string
	DevRoot  string
	IPMITool string
	CacheDir string // SDR cache file location (agent state dir)

	sdrCached bool
}

// NewSensors detects ipmitool.
func NewSensors(cacheDir string) *Sensors {
	return &Sensors{Run: ExecRunner, SysRoot: "/sys", DevRoot: "/dev", IPMITool: LookTool("ipmitool"), CacheDir: cacheDir}
}

func (s *Sensors) ipmiDevice() bool {
	for _, p := range []string{"ipmi0", "ipmi/0", "ipmidev/0"} {
		if _, err := os.Stat(filepath.Join(s.DevRoot, p)); err == nil {
			return true
		}
	}
	return false
}

// Collect reads all sensors.
func (s *Sensors) Collect(ctx context.Context, now time.Time) *model.SensorReport {
	r := &model.SensorReport{TS: now.UnixMilli(), Source: "none"}
	if s.IPMITool != "" && s.ipmiDevice() {
		args := []string{"-c", "sdr", "list"}
		cache := filepath.Join(s.CacheDir, "sdr.cache")
		// Dumping the SDR repository once makes later reads much faster.
		if !s.sdrCached && s.CacheDir != "" {
			if _, code, err := s.Run(ctx, s.IPMITool, "sdr", "dump", cache); err == nil && code == 0 {
				s.sdrCached = true
			}
		}
		if s.sdrCached {
			args = append([]string{"-S", cache}, args...)
		}
		out, code, err := s.Run(ctx, s.IPMITool, args...)
		if err == nil && code == 0 {
			r.Source = "ipmitool"
			r.Temps, r.Fans, r.PSUs = parseIPMICSV(string(out))
			return r
		}
		r.Error = "ipmitool: " + errText(err, code)
		s.sdrCached = false
	}
	temps, fans := readHwmon(s.SysRoot)
	if len(temps)+len(fans) > 0 {
		r.Source = "hwmon"
		r.Temps, r.Fans = temps, fans
	}
	return r
}

// parseIPMICSV parses `ipmitool -c sdr list`. Rows are
// "name,value,unit,status", e.g.
//
//	01-Inlet Ambient,22,degrees C,ok
//	Fan 1,25.48,percent,ok
//	Power Supply 1,0x00,discrete,ok
//
// Written from ipmitool's documented CSV output; to be confirmed on our
// servers.
func parseIPMICSV(out string) (temps, fans, psus []model.Sensor) {
	rd := csv.NewReader(strings.NewReader(out))
	rd.FieldsPerRecord = -1
	recs, _ := rd.ReadAll()
	for _, f := range recs {
		if len(f) < 4 {
			continue
		}
		name, val, unit, st := strings.TrimSpace(f[0]), strings.TrimSpace(f[1]), strings.TrimSpace(f[2]), strings.TrimSpace(f[3])
		status := ipmiStatus(st)
		v, verr := strconv.ParseFloat(val, 64)
		lname := strings.ToLower(name)
		switch {
		case unit == "degrees C":
			if verr != nil || st == "ns" {
				continue
			}
			temps = append(temps, model.Sensor{Name: name, Value: v, Unit: "C", Status: status})
		case strings.Contains(lname, "fan") && (unit == "RPM" || unit == "percent"):
			if verr != nil || st == "ns" {
				continue
			}
			u := "RPM"
			if unit == "percent" {
				u = "%"
			}
			fans = append(fans, model.Sensor{Name: name, Value: v, Unit: u, Status: status})
		case strings.Contains(lname, "power supply") || strings.HasPrefix(lname, "ps") && strings.Contains(lname, "status"):
			s := model.Sensor{Name: name, Status: status, Detail: st}
			if verr == nil && unit == "Watts" {
				s.Value, s.Unit = v, "W"
			}
			psus = append(psus, s)
		}
	}
	return
}

func ipmiStatus(s string) string {
	switch strings.ToLower(s) {
	case "ok":
		return "ok"
	case "nc":
		return "warning"
	case "cr", "nr":
		return "critical"
	}
	return "unknown"
}

// readHwmon reads /sys/class/hwmon/*: temp*_input (m°C) and fan*_input (RPM).
func readHwmon(sysRoot string) (temps, fans []model.Sensor) {
	base := filepath.Join(sysRoot, "class/hwmon")
	ents, _ := os.ReadDir(base)
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	for _, e := range ents {
		dir := filepath.Join(base, e.Name())
		chip := read(filepath.Join(dir, "name"))
		files, _ := filepath.Glob(filepath.Join(dir, "*_input"))
		sort.Strings(files)
		for _, f := range files {
			b := filepath.Base(f)
			pfx := strings.TrimSuffix(b, "_input")
			raw, err := strconv.ParseFloat(read(f), 64)
			if err != nil {
				continue
			}
			label := read(filepath.Join(dir, pfx+"_label"))
			if label == "" {
				label = pfx
			}
			name := chip + ": " + label
			switch {
			case strings.HasPrefix(pfx, "temp"):
				s := model.Sensor{Name: name, Value: raw / 1000, Unit: "C", Status: "ok"}
				if crit, err := strconv.ParseFloat(read(filepath.Join(dir, pfx+"_crit")), 64); err == nil && crit > 0 && raw >= crit {
					s.Status = "critical"
				} else if mx, err := strconv.ParseFloat(read(filepath.Join(dir, pfx+"_max")), 64); err == nil && mx > 0 && raw >= mx {
					s.Status = "warning"
				}
				temps = append(temps, s)
			case strings.HasPrefix(pfx, "fan"):
				fans = append(fans, model.Sensor{Name: name, Value: raw, Unit: "RPM", Status: "ok"})
			}
		}
	}
	return
}
