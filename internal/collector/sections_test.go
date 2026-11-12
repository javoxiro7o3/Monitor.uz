package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// NOTE: the tool outputs below are hand-written from the documented
// formats of ssacli / smartctl -j / ipmitool -c / mdstat, not captured on
// our HPE servers. Replace them with real captures when available.

const ssacliSample = `
Smart Array P408i-a SR Gen10 in Slot 0 (Embedded)
   Bus Interface: PCI
   Slot: 0
   Serial Number: PEYHB0ARH8Z123
   Controller Status: OK
   Cache Status: OK
   Battery/Capacitor Status: OK
   Controller Temperature (C): 51

   Internal Drive Cage at Port 1I, Box 1, OK
      Drive Bays: 4

   Array: A
      Interface Type: SAS
      Status: OK

      Logical Drive: 1
         Size: 558.88 GB
         Fault Tolerance: 1
         Status: OK
         Disk Name: /dev/sda
         Mount Points: None

      physicaldrive 1I:1:1
         Port: 1I
         Box: 1
         Bay: 1
         Status: OK
         Drive Type: Data Drive
         Interface Type: SAS
         Size: 600 GB
         Rotational Speed: 10000
         Serial Number: S7M0AAA1
         Model: HP      EG0600JETKA
         Current Temperature (C): 28
         Maximum Temperature (C): 39

      physicaldrive 1I:1:2
         Bay: 2
         Status: OK
         Drive Type: Data Drive
         Interface Type: SAS
         Size: 600 GB
         Serial Number: S7M0AAA2
         Model: HP      EG0600JETKA
         Current Temperature (C): 29

   Array: B
      Interface Type: Solid State SATA
      Status: Failed Physical Drive

      Logical Drive: 2
         Size: 3.49 TB
         Fault Tolerance: 5
         Status: Interim Recovery Mode
         Disk Name: /dev/sdb

      physicaldrive 1I:1:3
         Bay: 3
         Status: OK
         Interface Type: Solid State SATA
         Size: 1.9 TB
         Serial Number: SSD3
         Model: ATA     MK001920GWXFK
         Current Temperature (C): 31

      physicaldrive 1I:1:4
         Bay: 4
         Status: Failed
         Interface Type: Solid State SATA
         Size: 1.9 TB
         Serial Number: SSD4
         Model: ATA     MK001920GWXFK

      physicaldrive 2I:1:5
         Bay: 5
         Status: OK
         Drive Type: Spare Drive
         Interface Type: Solid State SATA
         Size: 1.9 TB
         Serial Number: SSD5

   unassigned

      physicaldrive 2I:1:6
         Bay: 6
         Status: OK
         Interface Type: SAS
         Size: 1.2 TB
         Serial Number: UN6

   SEP (Vendor ID HPE, Model Smart Adapter) 379
      Device Number: 379
`

func TestParseSSACLI(t *testing.T) {
	ctrls, lds, pds := parseSSACLI(ssacliSample)
	if len(ctrls) != 1 || ctrls[0].Name != "Smart Array P408i-a SR Gen10" || ctrls[0].Status != "OK" || ctrls[0].Battery != "OK" || ctrls[0].Slot != "0" {
		t.Fatalf("controller: %+v", ctrls)
	}
	if len(lds) != 2 {
		t.Fatalf("logical: %+v", lds)
	}
	if lds[0].RAID != "1" || lds[0].State != "ok" || lds[0].Device != "/dev/sda" || strings.Join(lds[0].Members, ",") != "1I:1:1,1I:1:2" {
		t.Errorf("ld1: %+v", lds[0])
	}
	if lds[1].RAID != "5" || lds[1].State != "degraded" || strings.Join(lds[1].Members, ",") != "1I:1:3,1I:1:4" || strings.Join(lds[1].Spares, ",") != "2I:1:5" {
		t.Errorf("ld2: %+v", lds[1])
	}
	if len(pds) != 6 {
		t.Fatalf("physical: %d", len(pds))
	}
	byID := map[string]int{}
	for i, p := range pds {
		byID[p.ID] = i
	}
	p1 := pds[byID["1I:1:1"]]
	if p1.Model != "HP EG0600JETKA" || p1.Size != 600e9 || p1.Type != "HDD" || p1.Temp != 28 || p1.MaxTemp != 39 || p1.Role != "data" {
		t.Errorf("pd1: %+v", p1)
	}
	if p := pds[byID["1I:1:4"]]; p.State != "failed" || p.Type != "SSD" {
		t.Errorf("pd4: %+v", p)
	}
	if p := pds[byID["2I:1:5"]]; p.Role != "spare" {
		t.Errorf("spare: %+v", p)
	}
	if p := pds[byID["2I:1:6"]]; p.Role != "unassigned" {
		t.Errorf("unassigned: %+v", p)
	}
}

func TestLogicalStates(t *testing.T) {
	cases := map[string]string{
		"OK": "ok", "Interim Recovery Mode": "degraded", "Recovering, 37% complete": "rebuilding",
		"Failed": "failed", "Ready for Rebuild": "degraded", "Something new": "unknown",
	}
	for in, want := range cases {
		if got, _ := logicalState(in); got != want {
			t.Errorf("%q => %q want %q", in, got, want)
		}
	}
	if _, p := logicalState("Recovering, 37% complete"); p != 37 {
		t.Errorf("progress %v", p)
	}
}

const smartATA = `{"smartctl":{"exit_status":0},"device":{"name":"/dev/sda","type":"sat","protocol":"ATA"},
"model_name":"Samsung SSD 870","serial_number":"S5X","user_capacity":{"bytes":1000204886016},
"rotation_rate":0,"temperature":{"current":33},"power_on_time":{"hours":12345},"smart_status":{"passed":true}}`

const smartLogical = `{"smartctl":{"exit_status":4},"device":{"name":"/dev/sda","type":"scsi","protocol":"SCSI"},
"scsi_vendor":"HP","scsi_product":"LOGICAL VOLUME","user_capacity":{"bytes":600000000000}}`

const smartFail = `{"smartctl":{"exit_status":8},"device":{"protocol":"ATA"},"model_name":"X","serial_number":"Y",
"rotation_rate":7200,"smart_status":{"passed":false}}`

const smartOpenErr = `{"smartctl":{"exit_status":2,"messages":[{"string":"Smartctl open device: /dev/sdz failed","severity":"error"}]}}`

func TestParseSmartJSON(t *testing.T) {
	d, logical, err := parseSmartJSON([]byte(smartATA), "/dev/sda")
	if err != nil || logical || d.Type != "SSD" || d.PowerOnHours != 12345 || d.Temp != 33 || d.State != "ok" || d.Size != 1000204886016 {
		t.Fatalf("ata: %+v %v %v", d, logical, err)
	}
	if _, logical, err := parseSmartJSON([]byte(smartLogical), "/dev/sda"); err != nil || !logical {
		t.Fatalf("logical volume not detected: %v", err)
	}
	if d, _, _ := parseSmartJSON([]byte(smartFail), "x"); d.State != "failed" || d.Type != "HDD" {
		t.Fatalf("fail: %+v", d)
	}
	if _, _, err := parseSmartJSON([]byte(smartOpenErr), "x"); err == nil {
		t.Fatal("open error not reported")
	}
}

func TestStorageSmartDirectViaCCISS(t *testing.T) {
	sys := t.TempDir()
	os.MkdirAll(filepath.Join(sys, "block/sda/device"), 0o755)
	os.MkdirAll(filepath.Join(sys, "block/sda1"), 0o755) // partition: no device link
	calls := 0
	run := func(_ context.Context, name string, args ...string) ([]byte, int, error) {
		calls++
		joined := strings.Join(args, " ")
		switch {
		case joined == "-a -j /dev/sda":
			return []byte(smartLogical), 4, nil
		case strings.Contains(joined, "cciss,0") || strings.Contains(joined, "cciss,1"):
			return []byte(strings.Replace(smartATA, "S5X", "SN"+joined[len(joined)-7:], 1)), 0, nil
		default:
			return []byte(smartOpenErr), 2, nil
		}
	}
	s := &Storage{Run: run, SysRoot: sys, ProcRoot: t.TempDir(), Smartctl: "smartctl"}
	r := s.Collect(context.Background(), time.Now())
	if r.Source != "smartctl" || len(r.Physical) != 2 || len(r.Logical) != 1 || len(r.Logical[0].Members) != 2 {
		t.Fatalf("report: %+v", r)
	}
}

func TestStorageNoTools(t *testing.T) {
	s := &Storage{Run: ExecRunner, SysRoot: t.TempDir(), ProcRoot: t.TempDir()}
	r := s.Collect(context.Background(), time.Now())
	if r.Source != "none" || len(r.Errors) == 0 {
		t.Fatalf("expected error: %+v", r)
	}
}

func TestParseMdstat(t *testing.T) {
	in := `Personalities : [raid1]
md0 : active raid1 sdb1[1] sda1[0]
      976630464 blocks super 1.2 [2/2] [UU]
      bitmap: 0/8 pages [0KB], 65536KB chunk

md1 : active raid1 sdd1[2] sdc1[0] sde1[3](S)
      976630464 blocks super 1.2 [2/1] [U_]
      [=>...................]  recovery =  8.5% (83136/976630464) finish=120.2min speed=135000K/sec

unused devices: <none>
`
	lds := parseMdstat(in)
	if len(lds) != 2 || lds[0].State != "ok" || lds[0].RAID != "1" || len(lds[0].Members) != 2 {
		t.Fatalf("md0: %+v", lds)
	}
	if lds[1].State != "rebuilding" || lds[1].Progress != 8.5 || len(lds[1].Spares) != 1 {
		t.Fatalf("md1: %+v", lds[1])
	}
}

func TestParseZpool(t *testing.T) {
	lds := parseZpoolList("rpool\t480103981056\tONLINE\ndata\t3840000000000\tDEGRADED\n")
	if len(lds) != 2 || lds[0].State != "ok" || lds[1].State != "degraded" || lds[0].Size != 480103981056 {
		t.Fatalf("%+v", lds)
	}
}

func TestParseIPMICSV(t *testing.T) {
	in := "01-Inlet Ambient,22,degrees C,ok\n02-CPU 1,40,degrees C,ok\n12-Drive,ns,degrees C,ns\nFan 1,25.48,percent,ok\n" +
		"Power Supply 1,0x00,discrete,ok\nPower Supply 2,0x00,discrete,cr\nCPU Utilization,10,unspecified,ok\n"
	temps, fans, psus := parseIPMICSV(in)
	if len(temps) != 2 || temps[0].Value != 22 || len(fans) != 1 || fans[0].Unit != "%" || len(psus) != 2 || psus[1].Status != "critical" {
		t.Fatalf("temps %+v fans %+v psus %+v", temps, fans, psus)
	}
}

func TestHwmon(t *testing.T) {
	sys := t.TempDir()
	d := filepath.Join(sys, "class/hwmon/hwmon0")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "name"), []byte("coretemp\n"), 0o644)
	os.WriteFile(filepath.Join(d, "temp1_input"), []byte("45000\n"), 0o644)
	os.WriteFile(filepath.Join(d, "temp1_label"), []byte("Package id 0\n"), 0o644)
	os.WriteFile(filepath.Join(d, "temp1_crit"), []byte("100000\n"), 0o644)
	temps, _ := readHwmon(sys)
	if len(temps) != 1 || temps[0].Value != 45 || temps[0].Name != "coretemp: Package id 0" {
		t.Fatalf("%+v", temps)
	}
}

func TestParseVMConf(t *testing.T) {
	qemu := `#web server
boot: order=scsi0
cores: 4
memory: 8192
name: vm-app-1
net0: virtio=BC:24:11:aa:bb:cc,bridge=vmbr0,firewall=1,tag=20
net1: e1000=BC:24:11:00:00:01,bridge=vmbr1
onboot: 1
ostype: l26
sockets: 2

[snap1]
name: old-name
net0: virtio=00:00:00:00:00:00,bridge=vmbr9
`
	v := parseVMConf("qemu", 101, []byte(qemu))
	if v.Name != "vm-app-1" || v.Cores != 8 || v.MemMax != 8192<<20 || !v.OnBoot || len(v.Nets) != 2 {
		t.Fatalf("qemu: %+v", v)
	}
	if n := v.Nets[0]; n.MAC != "BC:24:11:AA:BB:CC" || n.Bridge != "vmbr0" || n.Tag != 20 || !n.Firewall || n.Model != "virtio" {
		t.Fatalf("net0: %+v", n)
	}
	lxc := "arch: amd64\ncores: 2\nhostname: ct-db\nmemory: 2048\nnet0: name=eth0,bridge=vmbr0,firewall=1,gw=10.0.2.1,hwaddr=BC:24:11:11:22:33,ip=10.0.2.11/24,tag=30,type=veth\nostype: debian\n"
	c := parseVMConf("lxc", 200, []byte(lxc))
	if c.Name != "ct-db" || c.Cores != 2 || c.Nets[0].IP != "10.0.2.11/24" || c.Nets[0].MAC != "BC:24:11:11:22:33" || c.Nets[0].Iface != "eth0" {
		t.Fatalf("lxc: %+v", c)
	}
	if m := parseVMConf("qemu", 1, []byte("memory: current=4096\n")); m.MemMax != 4096<<20 {
		t.Fatalf("memory current=: %d", m.MemMax)
	}
}

func TestProxmoxCollect(t *testing.T) {
	root := t.TempDir()
	pve := filepath.Join(root, "pve")
	cg := filepath.Join(root, "cg")
	proc := filepath.Join(root, "proc")
	for _, d := range []string{"pve/qemu-server", "pve/lxc", "cg/qemu.slice/101.scope", "proc/net", "proc/4242"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(pve, "qemu-server/101.conf"), []byte("name: running-vm\ncores: 2\nmemory: 1024\nnet0: virtio=AA:BB:CC:DD:EE:01,bridge=vmbr0\n"), 0o644)
	os.WriteFile(filepath.Join(pve, "qemu-server/102.conf"), []byte("name: stopped-vm\n"), 0o644)
	os.WriteFile(filepath.Join(pve, "lxc/200.conf"), []byte("hostname: ct\n"), 0o644)
	os.WriteFile(filepath.Join(proc, "uptime"), []byte("1000.00 0\n"), 0o644)
	// pid 4242 started 400 s after boot (40000 ticks)
	os.WriteFile(filepath.Join(proc, "4242/stat"), []byte("4242 (kvm) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 40000 0 0\n"), 0o644)
	os.WriteFile(filepath.Join(cg, "qemu.slice/101.scope/cgroup.procs"), []byte("4242\n"), 0o644)
	write := func(usec, rb, tapRx uint64) {
		os.WriteFile(filepath.Join(cg, "qemu.slice/101.scope/cpu.stat"), []byte(fmt.Sprintf("usage_usec %d\nuser_usec 0\n", usec)), 0o644)
		os.WriteFile(filepath.Join(cg, "qemu.slice/101.scope/memory.current"), []byte("536870912\n"), 0o644)
		os.WriteFile(filepath.Join(cg, "qemu.slice/101.scope/io.stat"), []byte(fmt.Sprintf("8:0 rbytes=%d wbytes=0 rios=1 wios=0 dbytes=0 dios=0\n", rb)), 0o644)
		os.WriteFile(filepath.Join(proc, "net/dev"), []byte(fmt.Sprintf("h\nh\ntap101i0: %d 1 0 0 0 0 0 0 0 1 0 0 0 0 0 0\n", tapRx)), 0o644)
	}
	p := &Proxmox{PVEDir: pve, CgroupDir: cg, ProcRoot: proc}
	write(0, 0, 0)
	t0 := time.Now()
	p.Collect(t0)
	// 2 s later: 2 s of CPU time on 2 vCPUs = 50 %, 2 MB read => 1 MB/s,
	// 4000 bytes into the tap from the VM => VM transmitted 2000 B/s.
	write(2_000_000, 2_000_000, 4000)
	r := p.Collect(t0.Add(2 * time.Second))
	if len(r.VMs) != 3 {
		t.Fatalf("vms: %+v", r.VMs)
	}
	v := r.VMs[0]
	if v.VMID != 101 || v.Status != "running" || v.CPU != 50 || v.ReadBps != 1e6 || v.NetTxBps != 2000 || v.MemUsed != 512<<20 || v.Uptime != 600 {
		t.Fatalf("vm101: %+v", v)
	}
	if r.VMs[1].Status != "stopped" || r.VMs[2].Type != "lxc" || r.VMs[2].Status != "stopped" {
		t.Fatalf("others: %+v", r.VMs[1:])
	}
}

func TestProcsReal(t *testing.T) {
	p := NewProcs()
	p.Collect(time.Now())
	time.Sleep(100 * time.Millisecond)
	r := p.Collect(time.Now())
	if r.Total == 0 || len(r.Top) == 0 || r.Top[0].Name == "" {
		t.Fatalf("procs: %+v", r)
	}
}

func TestParsePidStatWeirdName(t *testing.T) {
	ps, ok := parsePidStat([]byte("77 (a) b (c)) S 1 1 1 0 -1 0 0 0 0 0 150 50 0 0 20 0 1 0 100 1000 300 0\n"))
	if !ok || ps.name != "a) b (c)" || ps.ticks != 200 || ps.rss != 300 {
		t.Fatalf("%+v %v", ps, ok)
	}
}

// fake Docker daemon on a unix socket
func TestDockerCollect(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("unix sockets unavailable")
	}
	id := strings.Repeat("a", 64)
	mux := http.NewServeMux()
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "1" {
			t.Error("all=1 missing")
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"Id": id, "Names": []string{"/web"}, "Image": "nginx:1.25", "State": "running", "Status": "Up 2 hours"},
			{"Id": strings.Repeat("b", 64), "Names": []string{"/old"}, "Image": "busybox", "State": "exited", "Status": "Exited (1) 3 days ago"},
		})
	})
	mux.HandleFunc("/containers/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("non-GET request to docker")
		}
		json.NewEncoder(w).Encode(map[string]any{"RestartCount": 3, "State": map[string]any{"Pid": 0, "ExitCode": 1, "StartedAt": "2026-11-12T10:00:00.5Z"}, "HostConfig": map[string]any{"Memory": 0}})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	cg := filepath.Join(dir, "cg", "system.slice", "docker-"+id+".scope")
	os.MkdirAll(cg, 0o755)
	writeCG := func(usec uint64) {
		os.WriteFile(filepath.Join(cg, "cpu.stat"), []byte(fmt.Sprintf("usage_usec %d\n", usec)), 0o644)
		os.WriteFile(filepath.Join(cg, "memory.current"), []byte("1000\n"), 0o644)
		os.WriteFile(filepath.Join(cg, "memory.stat"), []byte("inactive_file 200\n"), 0o644)
	}
	d := &Docker{Socket: sock, CgroupDir: filepath.Join(dir, "cg"), ProcRoot: dir}
	writeCG(0)
	t0 := time.Now()
	d.Collect(context.Background(), t0)
	writeCG(500_000)
	r := d.Collect(context.Background(), t0.Add(time.Second))
	if r.Error != "" || len(r.Containers) != 2 {
		t.Fatalf("report: %+v", r)
	}
	old, web := r.Containers[0], r.Containers[1]
	if web.Name != "web" || web.CPU != 50 || web.MemUsed != 800 || web.Restarts != 3 || web.ID != strings.Repeat("a", 12) {
		t.Fatalf("web: %+v", web)
	}
	if old.State != "exited" || old.ExitCode != 1 {
		t.Fatalf("old: %+v", old)
	}
}
