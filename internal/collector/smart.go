package collector

import (
	"encoding/json"
	"fmt"
	"strings"

	"monitor.uz/internal/model"
)

// smartJSON is the subset of `smartctl -a -j` (smartmontools >= 7.0)
// that we use. Field names follow the documented JSON schema.
type smartJSON struct {
	Smartctl struct {
		ExitStatus int `json:"exit_status"`
		Messages   []struct {
			String   string `json:"string"`
			Severity string `json:"severity"`
		} `json:"messages"`
	} `json:"smartctl"`
	Device struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
	} `json:"device"`
	ModelName    string `json:"model_name"`
	ScsiModel    string `json:"scsi_model_name"`
	ScsiProduct  string `json:"scsi_product"`
	ScsiVendor   string `json:"scsi_vendor"`
	SerialNumber string `json:"serial_number"`
	UserCapacity struct {
		Bytes uint64 `json:"bytes"`
	} `json:"user_capacity"`
	NVMeCapacity uint64 `json:"nvme_total_capacity"`
	RotationRate *int   `json:"rotation_rate"`
	Temperature  struct {
		Current float64 `json:"current"`
	} `json:"temperature"`
	PowerOnTime struct {
		Hours int64 `json:"hours"`
	} `json:"power_on_time"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
}

// parseSmartJSON converts smartctl JSON into a PhysDisk. ok is false when
// smartctl could not open the device (exit bits 0-1).
func parseSmartJSON(b []byte, id string) (d model.PhysDisk, logicalVolume bool, err error) {
	var s smartJSON
	if err := json.Unmarshal(b, &s); err != nil {
		return d, false, fmt.Errorf("smartctl %s: %w", id, err)
	}
	if s.Smartctl.ExitStatus&3 != 0 {
		msg := "device open failed"
		if len(s.Smartctl.Messages) > 0 {
			msg = s.Smartctl.Messages[0].String
		}
		return d, false, fmt.Errorf("smartctl %s: %s", id, msg)
	}
	model := s.ModelName
	if model == "" {
		model = strings.TrimSpace(s.ScsiVendor + " " + firstNonEmpty(s.ScsiModel, s.ScsiProduct))
	}
	// HPE Smart Array exposes logical drives as "HP LOGICAL VOLUME".
	if strings.Contains(strings.ToUpper(model), "LOGICAL VOLUME") {
		return d, true, nil
	}
	d = model2disk(id, model, s)
	return d, false, nil
}

func model2disk(id, modelName string, s smartJSON) model.PhysDisk {
	d := model.PhysDisk{ID: id, Model: modelName, Serial: s.SerialNumber, Size: s.UserCapacity.Bytes, Role: "data"}
	if d.Size == 0 {
		d.Size = s.NVMeCapacity
	}
	switch {
	case strings.EqualFold(s.Device.Protocol, "NVMe"):
		d.Type = "NVMe"
	case s.RotationRate != nil && *s.RotationRate == 0:
		d.Type = "SSD"
	case s.RotationRate != nil:
		d.Type = "HDD"
	}
	d.Interface = s.Device.Protocol
	d.Temp = s.Temperature.Current
	d.PowerOnHours = s.PowerOnTime.Hours
	d.State, d.Status, d.SMART = "unknown", "", ""
	if s.SmartStatus != nil {
		if s.SmartStatus.Passed {
			d.SMART, d.State, d.Status = "PASSED", "ok", "OK"
		} else {
			d.SMART, d.State, d.Status = "FAILED", "failed", "SMART FAILED"
		}
	}
	// Exit bit 3: disk failing; bits 4-5: prefail/old-age attributes at or
	// below threshold (now or in the past).
	if s.Smartctl.ExitStatus&8 != 0 {
		d.State, d.Status = "failed", "SMART: disk failing"
	} else if s.Smartctl.ExitStatus&16 != 0 && d.State == "ok" {
		d.State, d.Status = "warning", "SMART: prefail attribute"
	}
	return d
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
