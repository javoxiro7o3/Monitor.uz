package collector

import (
	"regexp"
	"strconv"
	"strings"

	"monitor.uz/internal/model"
)

// parseSSACLI parses `ssacli ctrl all show config detail`.
//
// The output is an indentation tree (3 spaces per level):
//
//	Smart Array P408i-a SR Gen10 in Slot 0 (Embedded)
//	   Controller Status: OK
//	   Array: A
//	      Logical Drive: 1
//	         Fault Tolerance: 1
//	         Status: OK
//	         Disk Name: /dev/sda
//	      physicaldrive 1I:1:1
//	         Status: OK
//	   unassigned
//	      physicaldrive 1I:1:8
//
// The parser was written from the documented ssacli format and HPE sample
// outputs, NOT from output captured on our servers: it must be checked
// against real output before relying on it (see docs).
func parseSSACLI(out string) (ctrls []model.Controller, logical []model.LogicalDrv, phys []model.PhysDisk) {
	type node struct {
		indent int
		kind   string // ctrl, array, ld, pd, section
		idx    int
	}
	var stack []node
	var curCtrl string
	var section string             // array, unassigned, hba, other (controller level)
	arrayLDs := map[string][]int{} // ctrl/array -> logical indexes
	arrayPDs := map[string][]int{} // ctrl/array -> physical indexes
	var curArray string

	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, " \r\t")
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}

		switch {
		case indent == 0 && strings.Contains(text, " in Slot "):
			name, rest, _ := strings.Cut(text, " in Slot ")
			slot := strings.Fields(rest)
			c := model.Controller{Name: strings.TrimSpace(name), Status: "unknown"}
			if len(slot) > 0 {
				c.Slot = slot[0]
			}
			ctrls = append(ctrls, c)
			curCtrl = c.Name
			if c.Slot != "" {
				curCtrl += " (slot " + c.Slot + ")"
			}
			section = ""
			stack = []node{{indent, "ctrl", len(ctrls) - 1}}
			continue
		case strings.HasPrefix(text, "Array: "):
			section = "array"
			curArray = curCtrl + "/" + strings.TrimPrefix(text, "Array: ")
			stack = append(stack, node{indent, "array", 0})
			continue
		case text == "unassigned" || text == "Unassigned":
			section = "unassigned"
			stack = append(stack, node{indent, "section", 0})
			continue
		case strings.HasPrefix(text, "HBA Drives"):
			section = "hba"
			stack = append(stack, node{indent, "section", 0})
			continue
		case strings.HasPrefix(text, "Logical Drive: "):
			ld := model.LogicalDrv{Controller: curCtrl, ID: strings.TrimPrefix(text, "Logical Drive: "), State: "unknown"}
			logical = append(logical, ld)
			arrayLDs[curArray] = append(arrayLDs[curArray], len(logical)-1)
			stack = append(stack, node{indent, "ld", len(logical) - 1})
			continue
		case strings.HasPrefix(text, "physicaldrive "):
			pd := model.PhysDisk{ID: strings.Fields(text)[1], Controller: curCtrl, State: "unknown"}
			switch section {
			case "array":
				pd.Role = "data"
			case "unassigned":
				pd.Role = "unassigned"
			case "hba":
				pd.Role = "hba"
			}
			phys = append(phys, pd)
			if section == "array" {
				arrayPDs[curArray] = append(arrayPDs[curArray], len(phys)-1)
			}
			stack = append(stack, node{indent, "pd", len(phys) - 1})
			continue
		}
		if indent <= 3 && !strings.Contains(text, ":") {
			// Other controller-level blocks (drive cages, SEP, ...).
			section = "other"
			stack = append(stack, node{indent, "section", 0})
			continue
		}

		k, v, ok := strings.Cut(text, ":")
		if !ok || len(stack) == 0 {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		top := stack[len(stack)-1]
		switch top.kind {
		case "ctrl":
			c := &ctrls[top.idx]
			switch k {
			case "Controller Status":
				c.Status = v
			case "Cache Status":
				c.Cache = v
			case "Battery/Capacitor Status":
				c.Battery = v
			case "Serial Number":
				c.Serial = v
			}
		case "ld":
			ld := &logical[top.idx]
			switch k {
			case "Size":
				ld.Size = parseSize(v)
			case "Fault Tolerance":
				ld.RAID = v
			case "Status":
				ld.Status = v
				ld.State, ld.Progress = logicalState(v)
			case "Disk Name":
				ld.Device = strings.Fields(v + " ")[0]
			}
		case "pd":
			pd := &phys[top.idx]
			switch k {
			case "Status":
				pd.Status = v
				pd.State = physState(v)
			case "Drive Type":
				if strings.Contains(v, "Spare") {
					pd.Role = "spare"
				}
			case "Interface Type":
				pd.Interface = v
				if strings.Contains(v, "Solid State") || strings.Contains(v, "NVMe") {
					pd.Type = "SSD"
				} else if pd.Type == "" {
					pd.Type = "HDD"
				}
			case "Size":
				pd.Size = parseSize(v)
			case "Serial Number":
				pd.Serial = v
			case "Model":
				pd.Model = strings.Join(strings.Fields(v), " ")
			case "Bay":
				pd.Bay = v
			case "Current Temperature (C)":
				pd.Temp, _ = strconv.ParseFloat(v, 64)
			case "Maximum Temperature (C)":
				pd.MaxTemp, _ = strconv.ParseFloat(v, 64)
			case "Power On Hours":
				pd.PowerOnHours, _ = strconv.ParseInt(strings.Fields(v + " ")[0], 10, 64)
			}
		}
	}

	// Members: every data/spare drive of the array the logical drive is in.
	for arr, lds := range arrayLDs {
		var members, spares []string
		for _, pi := range arrayPDs[arr] {
			if phys[pi].Role == "spare" {
				spares = append(spares, phys[pi].ID)
			} else {
				members = append(members, phys[pi].ID)
			}
		}
		for _, li := range lds {
			logical[li].Members = members
			logical[li].Spares = spares
		}
	}
	return ctrls, logical, phys
}

var progressRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*%`)

func logicalState(s string) (string, float64) {
	l := strings.ToLower(s)
	prog := 0.0
	if m := progressRe.FindStringSubmatch(s); m != nil {
		prog, _ = strconv.ParseFloat(m[1], 64)
	}
	switch {
	case l == "ok":
		return "ok", 0
	case strings.Contains(l, "interim recovery") || strings.Contains(l, "degraded") || strings.Contains(l, "ready for rebuild"):
		return "degraded", 0
	case strings.Contains(l, "recovering") || strings.Contains(l, "rebuild") || strings.Contains(l, "transforming") || strings.Contains(l, "expanding"):
		return "rebuilding", prog
	case strings.Contains(l, "fail"):
		return "failed", 0
	}
	return "unknown", 0
}

func physState(s string) string {
	l := strings.ToLower(s)
	switch {
	case l == "ok":
		return "ok"
	case strings.Contains(l, "predictive"):
		return "warning"
	case strings.Contains(l, "fail"):
		return "failed"
	case strings.Contains(l, "rebuild"):
		return "rebuilding"
	}
	return "unknown"
}

// parseSize converts "600 GB", "1.8 TB", "558.88 GB", "300 MB" to bytes
// (decimal units, as ssacli prints them).
func parseSize(s string) uint64 {
	f := strings.Fields(s)
	if len(f) < 2 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{"KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15}[strings.ToUpper(f[1])]
	if mult == 0 {
		return 0
	}
	return uint64(v * mult)
}
