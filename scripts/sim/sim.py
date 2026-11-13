#!/usr/bin/env python3
"""Test-only simulation of 3 Proxmox hosts and 1 Docker guest for Monitor.uz.

setup:   creates per-host fake /etc/pve, /sys/class/net, cgroup dirs and fake
         ssacli/smartctl/ipmitool scripts under ROOT.
run:     keeps cgroup counters moving and serves a fake Docker API socket and a
         webhook receiver (logs JSON posts).
"""
import json, os, random, socket, sys, threading, time, http.server, socketserver

ROOT = os.path.dirname(os.path.abspath(__file__))
H = os.path.join(ROOT, "hosts")
MAC0 = open("/sys/class/net/eth0/address").read().strip().upper()

HOSTS = {
  "srv-01": {"vms": [
      (100, "qemu", "vm-edge", 4, 4096, [("vmbr0", 0, "BC:24:11:01:00:01"), ("vmbr1", 0, "BC:24:11:01:00:02")], True, "OPNsense"),
      (101, "qemu", "vm-proxy", 2, 2048, [("vmbr0", 0, "BC:24:11:01:01:01")], True, "nginx"),
      (102, "lxc", "ct-dns", 1, 512, [("vmbr0", 0, "BC:24:11:01:02:01")], True, "pihole"),
      (103, "qemu", "vm-mail-test", 2, 4096, [("vmbr0", 10, "BC:24:11:01:03:01")], False, ""),
      (110, "lxc", "ct-ntp", 1, 256, [("vmbr0", 0, "BC:24:11:01:10:01")], True, ""),
  ], "stopped": {103}, "raid": "ok"},
  "srv-02": {"vms": [
      (200, "qemu", "vm-app-1", 8, 16384, [("vmbr0", 20, MAC0)], True, "docker"),
      (201, "qemu", "vm-app-2", 8, 16384, [("vmbr0", 20, "BC:24:11:02:01:01")], True, "docker"),
      (202, "qemu", "vm-app-3", 8, 16384, [("vmbr0", 20, "BC:24:11:02:02:01")], True, "docker"),
      (203, "lxc", "ct-queue", 2, 2048, [("vmbr0", 20, "BC:24:11:02:03:01")], True, ""),
      (204, "qemu", "vm-build", 4, 8192, [("vmbr0", 30, "BC:24:11:02:04:01")], True, ""),
      (299, "qemu", "tpl-debian12", 2, 2048, [("vmbr0", 0, "BC:24:11:02:99:01")], False, "template"),
  ], "stopped": {204}, "raid": "ok"},
  "srv-03": {"vms": [
      (300, "qemu", "vm-db-1", 8, 32768, [("vmbr0", 30, "BC:24:11:03:00:01")], True, "postgres"),
      (301, "qemu", "vm-db-2", 8, 32768, [("vmbr0", 30, "BC:24:11:03:01:01")], True, "postgres"),
      (302, "lxc", "ct-cache", 2, 4096, [("vmbr0", 30, "BC:24:11:03:02:01")], True, "redis"),
  ], "stopped": set(), "raid": "degraded"},
}

SSACLI_OK = open(os.path.join(ROOT, "ssacli-ok.txt")).read() if os.path.exists(os.path.join(ROOT, "ssacli-ok.txt")) else ""


def ssacli_text(state):
    drives = []
    for bay in range(1, 9):
        st = "OK"
        if state == "degraded" and bay == 5:
            st = "Failed"
        size = "1.2 TB" if bay <= 2 else "1.8 TB"
        role = "Spare Drive" if bay == 8 else "Data Drive"
        drives.append((bay, st, size, role))
    ld2 = "Interim Recovery Mode" if state == "degraded" else "OK"
    out = ["", "Smart Array P408i-a SR Gen10 in Slot 0 (Embedded)", "   Bus Interface: PCI", "   Slot: 0",
           "   Serial Number: PEYHB0ARH8Z%03d" % random.randint(0, 999), "   Controller Status: OK", "   Cache Status: OK",
           "   Battery/Capacitor Status: OK", "", "   Array: A", "      Interface Type: SAS", "      Status: OK", "",
           "      Logical Drive: 1", "         Size: 1.09 TB", "         Fault Tolerance: 1", "         Status: OK",
           "         Disk Name: /dev/sda ", ""]
    def pd(bay, st, size, role):
        return ["      physicaldrive 1I:1:%d" % bay, "         Port: 1I", "         Box: 1", "         Bay: %d" % bay,
                "         Status: %s" % st, "         Drive Type: %s" % role, "         Interface Type: SAS",
                "         Size: %s" % size, "         Rotational Speed: 10000", "         Serial Number: WFK%05d" % (bay * 1111),
                "         Model: HP      EG001200JWJNQ", "         Current Temperature (C): %d" % random.randint(27, 36),
                "         Maximum Temperature (C): 41", ""]
    for b in drives[:2]:
        out += pd(*b)
    out += ["   Array: B", "      Interface Type: SAS", "      Status: %s" % ("Failed Physical Drive" if state == "degraded" else "OK"), "",
            "      Logical Drive: 2", "         Size: 7.27 TB", "         Fault Tolerance: 5", "         Status: %s" % ld2,
            "         Disk Name: /dev/sdb ", ""]
    for b in drives[2:]:
        out += pd(*b)
    out += ["   SEP (Vendor ID HPE, Model Smart Adapter) 379", "      Device Number: 379", ""]
    return "\n".join(out)


def w(path, text, mode=0o644):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(text)
    os.chmod(path, mode)


def setup():
    for name, h in HOSTS.items():
        base = os.path.join(H, name)
        pve = os.path.join(base, "pve")
        node = os.path.join(pve, "nodes", name)
        for d in ("qemu-server", "lxc"):
            os.makedirs(os.path.join(node, d), exist_ok=True)
        for link, target in (("qemu-server", "nodes/%s/qemu-server" % name), ("lxc", "nodes/%s/lxc" % name), ("local", "nodes/" + name)):
            p = os.path.join(pve, link)
            if not os.path.lexists(p):
                os.symlink(target, p)
        for vmid, typ, vname, cores, mem, nets, onboot, tag in h["vms"]:
            lines = []
            if typ == "qemu":
                lines += ["boot: order=scsi0", "cores: %d" % cores, "memory: %d" % mem, "name: " + vname, "ostype: l26", "sockets: 1"]
                for i, (br, vlan, mac) in enumerate(nets):
                    lines.append("net%d: virtio=%s,bridge=%s,firewall=1%s" % (i, mac, br, ",tag=%d" % vlan if vlan else ""))
            else:
                lines += ["arch: amd64", "cores: %d" % cores, "hostname: " + vname, "memory: %d" % mem, "ostype: debian"]
                for i, (br, vlan, mac) in enumerate(nets):
                    ip = "10.0.%d.%d/24" % (vlan // 10, vmid % 100 + 10)
                    lines.append("net%d: name=eth%d,bridge=%s,firewall=1,hwaddr=%s,ip=%s,type=veth%s" % (i, i, br, mac, ip, ",tag=%d" % vlan if vlan else ""))
            if onboot:
                lines.append("onboot: 1")
            if tag == "template":
                lines.append("template: 1")
            elif tag:
                lines.append("tags: " + tag)
            d = "qemu-server" if typ == "qemu" else "lxc"
            w(os.path.join(node, d, "%d.conf" % vmid), "\n".join(lines) + "\n")
            if vmid in h["stopped"] or tag == "template":
                continue
            cg = os.path.join(base, "cg", "qemu.slice" if typ == "qemu" else "lxc", ("%d.scope" % vmid) if typ == "qemu" else str(vmid))
            os.makedirs(cg, exist_ok=True)
            w(os.path.join(cg, "cgroup.procs"), "%d\n" % os.getppid())
            w(os.path.join(cg, "cpu.stat"), "usage_usec 0\n")
            w(os.path.join(cg, "memory.current"), "%d\n" % (mem * 1024 * 1024 * random.uniform(0.3, 0.8)))
            w(os.path.join(cg, "io.stat"), "8:0 rbytes=0 wbytes=0 rios=0 wios=0 dbytes=0 dios=0\n")
        os.makedirs(os.path.join(base, "cg", "lxc"), exist_ok=True)
        os.makedirs(os.path.join(base, "cg", "qemu.slice"), exist_ok=True)
        # fake /sys/class/net: eth0 is the real uplink, bridged into vmbr0
        net = os.path.join(base, "net")
        for ifname, kind, master in (("eth0", "phys", "vmbr0"), ("eno2", "phys", "bond0"), ("eno3", "phys", "bond0"), ("bond0", "bond", "vmbr1"), ("vmbr0", "bridge", ""), ("vmbr1", "bridge", "")):
            d = os.path.join(net, ifname)
            os.makedirs(d, exist_ok=True)
            w(os.path.join(d, "address"), (MAC0 if ifname == "eth0" else "3c:fd:fe:%02x:00:%02x" % (int(name[-1]), len(ifname))).lower() + "\n")
            w(os.path.join(d, "operstate"), "up\n")
            if kind == "phys":
                os.makedirs(os.path.join(d, "device"), exist_ok=True)
                w(os.path.join(d, "speed"), "10000\n")
            if kind == "bridge":
                os.makedirs(os.path.join(d, "bridge"), exist_ok=True)
                w(os.path.join(d, "bridge", "vlan_filtering"), "1\n" if ifname == "vmbr0" else "0\n")
                os.makedirs(os.path.join(d, "brif"), exist_ok=True)
            if kind == "bond":
                os.makedirs(os.path.join(d, "bonding"), exist_ok=True)
            if master:
                p = os.path.join(d, "master")
                if not os.path.lexists(p):
                    os.symlink("../" + master, p)
                os.makedirs(os.path.join(net, master, "brif") if master.startswith("vmbr") else os.path.join(net, master), exist_ok=True)
                if master.startswith("vmbr"):
                    open(os.path.join(net, master, "brif", ifname), "w").close()
        # fake tools
        tools = os.path.join(base, "tools")
        w(os.path.join(tools, "ssacli.txt"), ssacli_text(h["raid"]))
        w(os.path.join(tools, "ssacli"), "#!/bin/sh\ncat %s/ssacli.txt\n" % tools, 0o755)
        smart = '''#!/bin/sh
# fake smartctl: answers -d cciss,N for N<8 with the serial ssacli prints
for a in "$@"; do case "$a" in cciss,*) n=${a#cciss,};; esac; done
if [ -z "$n" ]; then echo '{"smartctl":{"exit_status":4},"device":{"protocol":"SCSI"},"scsi_vendor":"HP","scsi_product":"LOGICAL VOLUME"}'; exit 4; fi
if [ "$n" -ge 8 ]; then echo '{"smartctl":{"exit_status":2,"messages":[{"string":"No such device"}]}}'; exit 2; fi
b=$((n+1)); sn=$(printf "WFK%%05d" $((b*1111)))
st=true; [ "%s" = degraded ] && [ $b -eq 5 ] && st=false
echo "{\\"smartctl\\":{\\"exit_status\\":0},\\"device\\":{\\"protocol\\":\\"SCSI\\"},\\"scsi_model_name\\":\\"EG001200JWJNQ\\",\\"serial_number\\":\\"$sn\\",\\"rotation_rate\\":10000,\\"temperature\\":{\\"current\\":30},\\"power_on_time\\":{\\"hours\\":$((21000+b*317))},\\"smart_status\\":{\\"passed\\":$st}}"
''' % h["raid"]
        w(os.path.join(tools, "smartctl"), smart, 0o755)
        ipmi = '''#!/bin/sh
case "$*" in *"sdr dump"*) for f in "$@"; do last=$f; done; touch "$last"; exit 0;; esac
t=$(( $(date +%%s) %% 7 ))
cat <<EOF
01-Inlet Ambient,$((21+t%%3)),degrees C,ok
02-CPU 1,$((44+t)),degrees C,ok
03-CPU 2,$((46+t)),degrees C,ok
04-P1 DIMM 1-6,36,degrees C,ok
12-Chipset,52,degrees C,ok
14-PS 1 Inlet,30,degrees C,ok
33-Battery Zone,%s,degrees C,%s
Fan 1,23.52,percent,ok
Fan 2,23.52,percent,ok
Fan 3,27.44,percent,ok
Fan 4,27.44,percent,ok
Power Supply 1,0x00,discrete,ok
Power Supply 2,0x00,discrete,%s
EOF
''' % (("63", "nc", "cr") if name == "srv-03" else ("33", "ok", "ok"))
        w(os.path.join(tools, "ipmitool"), ipmi, 0o755)
    # guest: docker cgroups
    g = os.path.join(H, "guest")
    for c in CONTAINERS:
        cg = os.path.join(g, "cg", "system.slice", "docker-%s.scope" % c["id"])
        os.makedirs(cg, exist_ok=True)
        w(os.path.join(cg, "cpu.stat"), "usage_usec 0\n")
        w(os.path.join(cg, "memory.current"), "%d\n" % c["mem"])
        w(os.path.join(cg, "memory.stat"), "inactive_file %d\n" % (c["mem"] // 10))
        w(os.path.join(cg, "io.stat"), "8:0 rbytes=0 wbytes=0\n")
    os.makedirs(os.path.join(g, "run"), exist_ok=True)
    print("setup done")


CONTAINERS = [
    {"id": ("%02x" % i) * 32, "name": n, "image": img, "cpu": cpu, "mem": mem << 20, "state": st, "restarts": r}
    for i, (n, img, cpu, mem, st, r) in enumerate([
        ("worker", "registry.lan/app/worker:2.4.1", 0.85, 1900, "running", 0),
        ("api", "registry.lan/app/api:2.4.1", 0.35, 620, "running", 0),
        ("web-proxy", "nginx:1.27", 0.05, 48, "running", 0),
        ("queue", "rabbitmq:3.13-management", 0.22, 410, "running", 2),
        ("cache", "redis:7.2", 0.09, 180, "running", 0),
        ("cron-report", "registry.lan/app/report:1.1", 0.0, 0, "exited", 0),
    ], start=1)
]


def updater():
    start = time.time()
    counters = {}
    while True:
        for name, h in HOSTS.items():
            for vmid, typ, vname, cores, mem, nets, onboot, tag in h["vms"]:
                cg = os.path.join(H, name, "cg", "qemu.slice" if typ == "qemu" else "lxc", ("%d.scope" % vmid) if typ == "qemu" else str(vmid))
                if not os.path.isdir(cg):
                    continue
                k = (name, vmid)
                c = counters.setdefault(k, [0, 0, 0])
                load = {"vm-app-3": 0.88, "vm-db-1": 0.55, "vm-edge": 0.18, "vm-app-1": 0.4}.get(vname, 0.06) * cores
                c[0] += int(1e6 * load * random.uniform(0.7, 1.3))
                c[1] += int(random.uniform(0, 4e6) * (3 if "db" in vname else 1))
                c[2] += int(random.uniform(0, 2e6))
                w(os.path.join(cg, "cpu.stat"), "usage_usec %d\n" % c[0])
                w(os.path.join(cg, "io.stat"), "8:0 rbytes=%d wbytes=%d rios=1 wios=1 dbytes=0 dios=0\n" % (c[1], c[2]))
        for ct in CONTAINERS:
            if ct["state"] != "running":
                continue
            cg = os.path.join(H, "guest", "cg", "system.slice", "docker-%s.scope" % ct["id"])
            c = counters.setdefault(ct["id"], [0, 0, 0])
            c[0] += int(1e6 * ct["cpu"] * random.uniform(0.7, 1.3))
            c[1] += int(random.uniform(0, 1e6))
            c[2] += int(random.uniform(0, 3e5))
            w(os.path.join(cg, "cpu.stat"), "usage_usec %d\n" % c[0])
            w(os.path.join(cg, "io.stat"), "8:0 rbytes=%d wbytes=%d\n" % (c[1], c[2]))
        time.sleep(1)


class DockerHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_GET(self):
        path = self.path.split("?")[0]
        if path == "/containers/json":
            body = [{"Id": c["id"], "Names": ["/" + c["name"]], "Image": c["image"], "State": c["state"],
                     "Status": "Up 3 days" if c["state"] == "running" else "Exited (1) 2 hours ago"} for c in CONTAINERS]
        elif path.startswith("/containers/") and path.endswith("/json"):
            cid = path.split("/")[2]
            c = next((x for x in CONTAINERS if x["id"] == cid), None)
            if not c:
                self.send_response(404); self.end_headers(); return
            body = {"RestartCount": c["restarts"], "State": {"Pid": os.getpid() if c["state"] == "running" else 0, "ExitCode": 0 if c["state"] == "running" else 1,
                    "StartedAt": time.strftime("%Y-%m-%dT%H:%M:%S.000Z", time.gmtime(time.time() - 3 * 86400 - hash(c["name"]) % 50000))},
                    "HostConfig": {"Memory": 2 << 30 if c["name"] == "worker" else 0}}
        else:
            self.send_response(404); self.end_headers(); return
        b = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)


class UnixHTTP(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True

    def get_request(self):
        req, _ = super().get_request()
        return req, ("local", 0)


class Hook(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        data = self.rfile.read(n)
        with open(os.path.join(ROOT, "webhook.log"), "a") as f:
            f.write(time.strftime("%H:%M:%S ") + self.path + " " + data.decode() + "\n")
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")


def run():
    os.makedirs("/run/mzsim", exist_ok=True)
    sock = "/run/mzsim/docker.sock"
    if os.path.exists(sock):
        os.remove(sock)
    srv = UnixHTTP(sock, DockerHandler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    hook = socketserver.ThreadingTCPServer(("127.0.0.1", 18065), Hook)
    threading.Thread(target=hook.serve_forever, daemon=True).start()
    if sys.argv[2:] == ["stop-cache"]:
        pass
    updater()


if __name__ == "__main__":
    {"setup": setup, "run": run}[sys.argv[1]]()
