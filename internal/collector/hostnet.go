package collector

import (
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"monitor.uz/internal/model"
)

// HostNet reads interface and bridge layout from /sys/class/net.
func HostNet(sysRoot string, now time.Time) *model.HostNetReport {
	r := &model.HostNetReport{TS: now.UnixMilli()}
	base := filepath.Join(sysRoot, "class/net")
	ents, err := os.ReadDir(base)
	if err != nil {
		return r
	}
	read := func(name, f string) string {
		b, err := os.ReadFile(filepath.Join(base, name, f))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	for _, e := range ents {
		name := e.Name()
		if skipIface(name) {
			continue
		}
		kind := ifaceKind(sysRoot, name)
		ifc := model.HostIface{
			Name: name, Kind: kind,
			MAC: strings.ToUpper(read(name, "address")),
			Up:  read(name, "operstate") == "up",
		}
		if sp, err := strconv.Atoi(read(name, "speed")); err == nil && sp > 0 {
			ifc.Speed = sp
		}
		if l, err := os.Readlink(filepath.Join(base, name, "master")); err == nil {
			ifc.Master = filepath.Base(l)
		}
		ifc.Addrs = ifaceAddrs(name)
		r.Ifaces = append(r.Ifaces, ifc)
		if kind == "bridge" {
			br := model.Bridge{Name: name, VLANAware: read(name, "bridge/vlan_filtering") == "1", Addrs: ifc.Addrs}
			if ports, err := os.ReadDir(filepath.Join(base, name, "brif")); err == nil {
				for _, p := range ports {
					if !skipIface(p.Name()) {
						br.Ports = append(br.Ports, p.Name())
					}
				}
			}
			sort.Strings(br.Ports)
			r.Bridges = append(r.Bridges, br)
		}
	}
	return r
}

func ifaceAddrs(name string) []string {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLinkLocalUnicast() {
			out = append(out, ipn.String())
		}
	}
	return out
}

// GuestIfaces lists a guest's interfaces with MAC and addresses (for
// matching it to its Proxmox VM/CT).
func GuestIfaces() []model.IfaceAddr {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []model.IfaceAddr
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 || len(i.HardwareAddr) == 0 ||
			strings.HasPrefix(i.Name, "veth") || strings.HasPrefix(i.Name, "docker") || strings.HasPrefix(i.Name, "br-") {
			continue
		}
		out = append(out, model.IfaceAddr{Name: i.Name, MAC: strings.ToUpper(i.HardwareAddr.String()), Addrs: ifaceAddrs(i.Name)})
	}
	return out
}
