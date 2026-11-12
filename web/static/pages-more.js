// Monitor.uz: VM/CT, guest internals, Docker, disks, network map, alerts.
"use strict";

(function () {
  const { $, $$, t, esc, fmt } = M;

  async function setMonitored(kind, key, value) {
    try {
      M.state.overrides = await M.api("/api/v1/monitoring", { method: "PUT", body: { kind, key, monitored: value } });
    } catch (e) {
      M.toast(e.message, "crit");
    }
  }
  document.addEventListener("change", (e) => {
    const el = e.target.closest("input[data-mon]");
    if (!el) return;
    setMonitored(el.dataset.mon, el.dataset.key, el.checked);
  });
  M.monToggle = (kind, key, on) =>
    `<label class="switch" title="${esc(t("mon.toggle"))}"><input type="checkbox" data-mon="${kind}" data-key="${esc(key)}" ${on ? "checked" : ""}><i></i></label>`;

  // ---------- VM table (shared) ----------
  M.vmTable = function (id, list, withHost) {
    const cols = [
      { label: t("vm.status"), sort: (x) => x.vm.status },
      { label: "ID", sort: (x) => x.vm.vmid },
      { label: t("vm.name"), sort: (x) => x.vm.name },
    ];
    if (withHost) cols.push({ label: t("vm.server"), sort: (x) => x.host.name });
    cols.push(
      { label: t("vm.type"), sort: (x) => x.vm.type },
      { label: "CPU", right: 1, sort: (x) => x.vm.cpu },
      { label: "RAM", right: 1, sort: (x) => x.vm.mem_used },
      { label: t("vm.disk_io"), right: 1, sort: (x) => x.vm.read_bps + x.vm.write_bps },
      { label: t("vm.net"), right: 1, sort: (x) => x.vm.net_rx_bps + x.vm.net_tx_bps },
      { label: t("vm.uptime"), right: 1, sort: (x) => x.vm.uptime || 0 },
      { label: t("vm.guest") },
      { label: t("vm.monitored") },
    );
    const rows = list.map((x) => {
      const v = x.vm, on = v.status === "running";
      const g = M.guestFor(x.host.id, v.vmid);
      const cells = [
        M.statusHTML(on ? "ok" : M.vmMonitored(x.host.id, v) ? "crit" : "off", t("vm.st." + v.status)),
        `<span class="num">${v.vmid}</span>`,
        `<b>${esc(v.name)}</b>${v.tags ? `<div class="dim">${esc(v.tags.replace(/;/g, ", "))}</div>` : ""}`,
      ];
      if (withHost) cells.push(esc(x.host.name));
      cells.push(
        `<span class="tag">${v.type === "lxc" ? "CT" : "VM"}</span>`,
        on ? `${fmt.pct(v.cpu)}<div class="dim">${esc(t("vm.cores", { n: v.cores || "?" }))}</div>` : "–",
        on ? `${fmt.bytes(v.mem_used)}<div class="dim">/ ${fmt.bytes(v.mem_max)}</div>` : `<span class="dim">${fmt.bytes(v.mem_max)}</span>`,
        on ? `${fmt.bytesRate(v.read_bps)}<div class="dim">${fmt.bytesRate(v.write_bps)}</div>` : "–",
        on ? `↓ ${fmt.bits(v.net_rx_bps)}<div class="dim">↑ ${fmt.bits(v.net_tx_bps)}</div>` : "–",
        on ? fmt.duration(v.uptime || 0) : "–",
        g ? M.statusHTML(M.online(g) ? "ok" : "off", g.reports && g.reports.docker ? t("vm.guest_docker", { n: (g.reports.docker.containers || []).length }) : t("vm.guest_yes")) : `<span class="dim">–</span>`,
        M.monToggle("vm", x.host.id + ":" + v.vmid, M.vmMonitored(x.host.id, v)),
      );
      return { cells, data: x, href: `#/vm/${x.host.id}/${v.vmid}`, cls: on ? "" : "row-dim" };
    });
    return M.table(id, cols, rows, { empty: t("vms.none"), defaultSort: { col: 1, dir: 1 } });
  };

  // ---------- VM/CT list ----------
  M.pages.vms = function (view) {
    let filter = { q: "", host: "", only: true };
    try { filter = Object.assign(filter, JSON.parse(sessionStorage.getItem("vmfilter") || "{}")); } catch (_) { /* ignore */ }
    M.setTitle(t("vms.title"), t("vms.subtitle"));
    view.innerHTML = `<div class="filters">
        <input type="search" id="vm-q" placeholder="${esc(t("filter.search"))}" value="${esc(filter.q)}">
        <select id="vm-host"><option value="">${esc(t("filter.all_servers"))}</option>${M.hosts().map((h) => `<option value="${h.id}" ${String(h.id) === filter.host ? "selected" : ""}>${esc(h.name)}</option>`).join("")}</select>
        <label class="check"><input type="checkbox" id="vm-only" ${filter.only ? "checked" : ""}>${esc(t("vms.only_monitored"))}</label>
      </div>
      <div class="card" id="vm-list"></div>
      <div class="section card" id="guest-unlinked"></div>`;
    const render = M.throttle(draw, 1000);
    function draw() {
      const q = filter.q.toLowerCase();
      const list = M.allVMs().filter((x) => !x.vm.template &&
        (!filter.host || String(x.host.id) === filter.host) &&
        (!filter.only || M.vmMonitored(x.host.id, x.vm)) &&
        (!q || x.vm.name.toLowerCase().includes(q) || String(x.vm.vmid).includes(q)));
      const all = M.allVMs().filter((x) => !x.vm.template).length;
      $("#vm-list").innerHTML = `<div class="card-head"><h2>${esc(t("vms.count", { n: list.length, all }))}</h2></div>` + M.vmTable("vms", list, true);
      const un = M.guests().filter((g) => !g.link);
      $("#guest-unlinked").hidden = !un.length;
      $("#guest-unlinked").innerHTML = `<div class="card-head"><h2>${esc(t("vms.unlinked"))}</h2><span class="note">${esc(t("vms.unlinked_note"))}</span></div>` +
        un.map((g) => `<a class="alert-item" href="#/guest/${g.id}">${M.statusHTML(M.health(g))}<div><b>${esc(g.name)}</b><div class="dim">${esc((g.info.ifaces || []).map((i) => i.mac).join(", "))}</div></div></a>`).join("");
      M.applyStyles(view);
    }
    const save = () => { try { sessionStorage.setItem("vmfilter", JSON.stringify(filter)); } catch (_) { /* ignore */ } };
    $("#vm-q").addEventListener("input", (e) => { filter.q = e.target.value; save(); render(true); });
    $("#vm-host").addEventListener("change", (e) => { filter.host = e.target.value; save(); render(true); });
    $("#vm-only").addEventListener("change", (e) => { filter.only = e.target.checked; save(); render(true); });
    draw();
    return { onReport: () => render(), onStatus: () => render(), onAgent: () => render(), onSnapshot: () => render(true), render: () => render(true) };
  };

  // ---------- guest internals (shared by VM page and guest page) ----------
  const GUEST_CHARTS = [
    { id: "gcpu", title: "chart.cpu_in", max: 100, f: (v, ax) => fmt.pct(v, ax), series: [["cpu.usage", "s.cpu_usage", "--s1"], ["cpu.iowait", "s.cpu_iowait", "--s2"]] },
    { id: "gmem", title: "chart.mem_in", max: 100, f: (v, ax) => fmt.pct(v, ax), series: [["mem.used_pct", "s.mem_used", "--s1"], ["mem.swap_pct", "s.mem_swap", "--s3"]] },
  ];
  M.guestSection = function (root, gid) {
    root.innerHTML = `<div class="kpis" data-g="kpis"></div><div data-g="charts"></div>
      <div class="section card"><div class="card-head"><h2>${esc(t("guest.containers"))}</h2><span class="note" data-g="dnote"></span></div><div data-g="docker"></div></div>
      <div class="section grid-2">
        <div class="card"><div class="card-head"><h2>${esc(t("guest.procs"))}</h2><span class="note" data-g="pnote"></span></div><div data-g="procs"></div></div>
        <div class="card"><div class="card-head"><h2>${esc(t("fs.title"))}</h2></div><div data-g="fs"></div></div>
      </div>`;
    const q = (n) => $(`[data-g="${n}"]`, root);
    const panel = M.chartPanel(q("charts"), gid, GUEST_CHARTS, { liveStep: 5000 });
    function draw() {
      const g = M.state.agents.get(gid);
      if (!g) return;
      const s = M.online(g) ? g.latest || {} : {};
      const r = M.reports(g);
      const n = M.guestNet(s);
      const items = [
        [t("kpi.cpu"), fmt.pct(s.cpu && s.cpu.usage), s.load ? "load " + fmt.num(s.load.l1) : ""],
        [t("kpi.ram"), fmt.pct(s.mem && s.mem.used_pct), s.mem ? fmt.bytes(s.mem.used) + " / " + fmt.bytes(s.mem.total) : ""],
        [t("kpi.net"), s.net ? fmt.bits(n.rx) : "–", s.net ? "↑ " + fmt.bits(n.tx) : ""],
        [t("kpi.disk_used"), fmt.pct(M.maxFS(s) && M.maxFS(s).used_pct), M.maxFS(s) ? M.maxFS(s).mount : ""],
        [t("guest.uptime"), s.uptime ? fmt.duration(s.uptime) : "–", g.info.os || ""],
      ];
      q("kpis").innerHTML = items.map(([l, v, nn]) => `<div class="card kpi"><div class="label">${esc(l)}</div><div class="value">${esc(v)}</div><div class="note">${esc(nn)}</div></div>`).join("");
      const d = r.docker;
      q("dnote").textContent = d ? t("guest.docker_note", { n: (d.containers || []).length, r: (d.containers || []).filter((c) => c.state === "running").length }) : "";
      q("docker").innerHTML = !d ? M.empty(t("guest.no_docker")) : d.error ? `<div class="banner neutral small">${esc(d.error)}</div>` :
        M.containerTable("g-" + gid, (d.containers || []).map((c) => ({ guest: g, c })), false);
      const p = r.procs;
      q("pnote").textContent = p ? t("guest.procs_note", { n: p.total }) : "";
      q("procs").innerHTML = !p ? M.empty() : M.table("g-procs-" + gid, [
        { label: "PID", sort: (x) => x.pid }, { label: t("proc.name"), sort: (x) => x.name }, { label: t("proc.user"), sort: (x) => x.user || "" },
        { label: "CPU", right: 1, sort: (x) => x.cpu }, { label: "RAM", right: 1, sort: (x) => x.rss }],
      p.top.map((x) => ({ data: x, cells: [`<span class="num">${x.pid}</span>`, `<b>${esc(x.name)}</b>`, esc(x.user || ""), fmt.pct(x.cpu), fmt.bytes(x.rss)] })),
      { defaultSort: { col: 3, dir: -1 } });
      q("fs").innerHTML = M.table("g-fs-" + gid, [{ label: t("fs.mount") }, { label: t("fs.size"), right: 1 }, { label: t("fs.used"), right: 1 }],
        (s.fs || []).map((f) => ({ cells: [`<b>${esc(f.mount)}</b><div class="dim mono">${esc(f.device)}</div>`, fmt.bytes(f.total), `${M.bar(f.used_pct, "disk")}<span class="num">${fmt.pct(f.used_pct)}</span>`] })));
      M.applyStyles(root);
    }
    const render = M.throttle(draw, 900);
    draw();
    return {
      onSample(id, s) { if (id === gid) { panel.push(s.ts, M.pick(s)); render(); } },
      onReport(id) { if (id === undefined || id === gid) render(); },
      onStatus(id) { if (id === gid) render(); },
      onBackfill(id) { if (id === gid) panel.reload(); },
      onTheme() { panel.redraw(); },
      render: () => render(true),
      destroy() { panel.destroy(); },
    };
  };

  // ---------- VM detail ----------
  M.pages.vm = function (view, hidStr, vmidStr) {
    const hid = +hidStr, vmid = +vmidStr;
    const key = (k) => `vm[${vmid}].${k}`;
    const find = () => {
      const h = M.state.agents.get(hid);
      const vm = h && ((M.reports(h).vms || {}).vms || []).find((v) => v.vmid === vmid);
      return { h, vm };
    };
    if (!find().vm) {
      M.setTitle(t("vm.not_found"));
      view.innerHTML = `<div class="card empty"><h2>${esc(t("vm.not_found"))}</h2><a class="link" href="#/vms">← ${esc(t("nav.vms"))}</a></div>`;
      return {};
    }
    view.innerHTML = `<div id="vm-kpis" class="kpis"></div><div id="vm-charts"></div>
      <div class="section grid-2"><div class="card"><div class="card-head"><h2>${esc(t("vm.nics"))}</h2></div><div id="vm-nics"></div></div>
      <div class="card"><div class="card-head"><h2>${esc(t("vm.config"))}</h2></div><div id="vm-conf"></div></div></div>
      <div class="section"><div class="section-title"><h2>${esc(t("guest.title"))}</h2><span class="note" id="g-note"></span></div><div id="guest"></div></div>`;
    const VM_CHARTS = [
      { id: "vcpu", title: "chart.cpu", max: 100, f: (v, ax) => fmt.pct(v, ax), series: [[key("cpu"), "s.cpu_usage", "--s1"]] },
      { id: "vmem", title: "chart.mem", max: null, f: (v) => fmt.bytes(v), series: [[key("mem"), "s.mem_used", "--s1"]] },
      { id: "vdisk", title: "chart.disk", max: null, f: (v) => fmt.bytesRate(v), series: [[key("read_bps"), "s.disk_read", "--s1"], [key("write_bps"), "s.disk_write", "--s2"]] },
      { id: "vnet", title: "chart.vmnet", max: null, f: (v) => fmt.bits(v), series: [[key("net_rx_bps"), "s.net_rx", "--s1"], [key("net_tx_bps"), "s.net_tx", "--s2"]] },
    ];
    const panel = M.chartPanel($("#vm-charts"), hid, VM_CHARTS, { liveStep: 5000 });
    let guest = null, guestId = null;
    let lastTS = 0;
    function draw() {
      const { h, vm } = find();
      if (!vm) return;
      const on = vm.status === "running";
      const mon = M.vmMonitored(hid, vm);
      M.setTitleHTML(`<a class="crumb" href="#/vms">← ${esc(t("nav.vms"))}</a>
        <span class="title-row"><span>${esc(vm.name)}</span>${M.statusHTML(on ? "ok" : mon ? "crit" : "off", t("vm.st." + vm.status))}${M.monToggle("vm", hid + ":" + vmid, mon)}<span class="dim small">${esc(t("vm.monitored"))}</span></span>`,
        `<div class="meta"><span><a class="link" href="#/server/${hid}">${esc(h.name)}</a></span><span>${vm.type === "lxc" ? "CT" : "VM"} ${vmid}</span>${vm.ostype ? `<span>${esc(vm.ostype)}</span>` : ""}${on ? `<span>${esc(t("server.uptime", { v: fmt.duration(vm.uptime || 0) }))}</span>` : ""}</div>`, vm.name);
      const items = [
        ["CPU", on ? fmt.pct(vm.cpu) : "–", t("vm.cores", { n: vm.cores || "?" })],
        ["RAM", on ? fmt.bytes(vm.mem_used) : "–", "/ " + fmt.bytes(vm.mem_max)],
        [t("kpi.disk"), on ? fmt.bytesRate(vm.read_bps) : "–", on ? t("s.disk_write") + " " + fmt.bytesRate(vm.write_bps) : ""],
        [t("kpi.net"), on ? fmt.bits(vm.net_rx_bps) : "–", on ? "↑ " + fmt.bits(vm.net_tx_bps) : ""],
      ];
      $("#vm-kpis").innerHTML = items.map(([l, v, n]) => `<div class="card kpi"><div class="label">${esc(l)}</div><div class="value">${esc(v)}</div><div class="note">${esc(n)}</div></div>`).join("");
      const g = M.guestFor(hid, vmid);
      const gIPs = {};
      if (g) for (const i of g.info.ifaces || []) gIPs[i.mac.toUpperCase()] = (i.addrs || []).join(", ");
      $("#vm-nics").innerHTML = M.table("vm-nics", [{ label: "" }, { label: "MAC" }, { label: "Bridge" }, { label: "VLAN" }, { label: "IP" }, { label: t("vm.firewall") }],
        (vm.nets || []).map((n) => ({ cells: [`<b>${esc(n.id)}</b><div class="dim">${esc(n.model || n.iface || "")}</div>`, `<span class="mono">${esc(n.mac)}</span>`,
          esc(n.bridge || "–"), n.tag ? esc(n.tag) : "–", `<span class="mono">${esc(gIPs[n.mac] || n.ip || "–")}</span>`, n.firewall ? t("yes") : t("no")] })));
      $("#vm-conf").innerHTML = `<div class="kv">
        <span>${esc(t("vm.cfg_cores"))}</span><b>${esc(vm.cores || "–")}</b>
        <span>${esc(t("vm.cfg_mem"))}</span><b>${esc(fmt.bytes(vm.mem_max))}</b>
        <span>${esc(t("vm.cfg_onboot"))}</span><b>${esc(vm.onboot ? t("yes") : t("no"))}</b>
        <span>${esc(t("vm.cfg_tags"))}</span><b>${esc(vm.tags || "–")}</b>
        <span>${esc(t("vm.cfg_mon"))}</span><b>${esc(mon ? t("yes") : t("no"))}</b></div>`;
      if (on && vm.cpu != null) {
        const ts = (M.reports(h).vms || {}).ts;
        if (ts && ts > lastTS) {
          lastTS = ts;
          panel.push(ts, { [key("cpu")]: vm.cpu, [key("mem")]: vm.mem_used, [key("read_bps")]: vm.read_bps, [key("write_bps")]: vm.write_bps, [key("net_rx_bps")]: vm.net_rx_bps, [key("net_tx_bps")]: vm.net_tx_bps });
        }
      }
      if (g && guestId !== g.id) {
        if (guest) guest.destroy();
        guestId = g.id;
        guest = M.guestSection($("#guest"), g.id);
      }
      $("#g-note").innerHTML = g ? `${M.statusHTML(M.health(g), t("guest.agent", { n: g.name }))}` : "";
      if (!g) {
        guestId = null;
        $("#guest").innerHTML = `<div class="card empty small-empty"><p>${esc(t("guest.none"))}</p><a class="btn" href="#/settings/agents">${esc(t("guest.install"))}</a></div>`;
      }
      M.applyStyles(view);
    }
    const render = M.throttle(draw, 900);
    draw();
    const call = (n, ...a) => guest && guest[n] && guest[n](...a);
    return {
      onReport(id, kind) { if (id === hid || id === undefined) render(); call("onReport", id, kind); },
      onSample(id, s) { call("onSample", id, s); },
      onStatus(id) { render(); call("onStatus", id); },
      onAgent() { render(); },
      onBackfill(id) { if (id === hid) panel.reload(); call("onBackfill", id); },
      onSnapshot() { render(true); panel.reload(); },
      onTheme() { panel.redraw(); call("onTheme"); },
      render() { render(true); call("render"); },
      destroy() { panel.destroy(); if (guest) guest.destroy(); },
    };
  };

  // ---------- guest without a matched VM ----------
  M.pages.guest = function (view, idStr) {
    const id = +idStr;
    const g = M.state.agents.get(id);
    if (!g) {
      M.setTitle(t("server.not_found"));
      return {};
    }
    if (g.link) { location.replace(`#/vm/${g.link.host_id}/${g.link.vmid}`); return {}; }
    M.setTitleHTML(`<a class="crumb" href="#/vms">← ${esc(t("nav.vms"))}</a><span class="title-row"><span>${esc(g.name)}</span>${M.statusHTML(M.health(g))}</span>`,
      `<div class="meta"><span>${esc(t("guest.unlinked"))}</span><span>${esc(g.info.os || "")}</span></div>`, g.name);
    const sec = M.guestSection(view, id);
    return sec;
  };

  // ---------- containers ----------
  M.containerTable = function (id, list, withPlace) {
    const cols = [
      { label: t("vm.status"), sort: (x) => x.c.state },
      { label: t("docker.name"), sort: (x) => x.c.name },
    ];
    if (withPlace) cols.push({ label: t("docker.where"), sort: (x) => M.guestLabel(x.guest) });
    cols.push(
      { label: "CPU", right: 1, sort: (x) => x.c.cpu },
      { label: "RAM", right: 1, sort: (x) => x.c.mem_used },
      { label: t("vm.net"), right: 1, sort: (x) => x.c.net_rx_bps + x.c.net_tx_bps },
      { label: "I/O", right: 1, sort: (x) => x.c.read_bps + x.c.write_bps },
      { label: t("docker.restarts"), right: 1, sort: (x) => x.c.restarts },
      { label: t("vm.uptime"), right: 1, sort: (x) => x.c.started || 0 },
      { label: t("vm.monitored") },
    );
    const rows = list.map((x) => {
      const c = x.c, on = c.state === "running";
      const mon = M.ctrMonitored(x.guest.id, c.name);
      const st = on ? (c.health === "unhealthy" ? "warn" : "ok") : c.state === "restarting" ? "warn" : (mon ? "crit" : "off");
      const cells = [
        M.statusHTML(st, t("docker.st." + c.state) + (c.health ? " · " + c.health : "")),
        `<b>${esc(c.name)}</b><div class="dim mono">${esc(c.image)}</div>`,
      ];
      if (withPlace) cells.push(`<a class="link" href="${x.guest.link ? `#/vm/${x.guest.link.host_id}/${x.guest.link.vmid}` : `#/guest/${x.guest.id}`}">${esc(M.guestLabel(x.guest))}</a>`);
      cells.push(
        on ? fmt.pct(c.cpu) : "–",
        on ? `${fmt.bytes(c.mem_used)}${c.mem_limit ? `<div class="dim">/ ${fmt.bytes(c.mem_limit)}</div>` : ""}` : "–",
        on ? `↓ ${fmt.bits(c.net_rx_bps)}<div class="dim">↑ ${fmt.bits(c.net_tx_bps)}</div>` : "–",
        on ? `${fmt.bytesRate(c.read_bps)}<div class="dim">${fmt.bytesRate(c.write_bps)}</div>` : "–",
        c.restarts ? `<span class="txt-warn">${c.restarts}</span>` : "0",
        on && c.started ? fmt.duration(M.now() / 1000 - c.started) : esc(c.status),
        M.monToggle("container", x.guest.id + ":" + c.name, mon),
      );
      return { cells, data: x, cls: on ? "" : "row-dim" };
    });
    return M.table(id, cols, rows, { empty: t("docker.none"), defaultSort: { col: withPlace ? 3 : 2, dir: -1 } });
  };

  M.pages.docker = function (view) {
    let q = "", stf = "";
    M.setTitle(t("docker.title"), t("docker.subtitle"));
    view.innerHTML = `<div class="kpis" id="d-kpis"></div><div class="filters">
      <input type="search" id="d-q" placeholder="${esc(t("filter.search_ctr"))}">
      <select id="d-st"><option value="">${esc(t("filter.all_states"))}</option><option value="running">${esc(t("docker.st.running"))}</option><option value="stopped">${esc(t("filter.stopped"))}</option></select>
      </div><div class="card" id="d-list"></div>`;
    function draw() {
      const all = M.containers();
      const ql = q.toLowerCase();
      const list = all.filter((x) => (!ql || x.c.name.toLowerCase().includes(ql) || x.c.image.toLowerCase().includes(ql) || M.guestLabel(x.guest).toLowerCase().includes(ql)) &&
        (!stf || (stf === "running" ? x.c.state === "running" : x.c.state !== "running")));
      const running = all.filter((x) => x.c.state === "running");
      const cpu = running.reduce((s, x) => s + x.c.cpu, 0), mem = running.reduce((s, x) => s + x.c.mem_used, 0);
      const restarts = all.filter((x) => x.c.restarts > 0).length;
      $("#d-kpis").innerHTML = [
        [t("docker.k_total"), all.length, t("docker.k_hosts", { n: M.guests().filter((g) => g.reports && g.reports.docker).length })],
        [t("docker.k_running"), running.length, t("docker.k_stopped", { n: all.length - running.length })],
        [t("docker.k_cpu"), fmt.pct(cpu), t("docker.k_cpu_note")],
        [t("docker.k_mem"), fmt.bytes(mem), restarts ? t("docker.k_restarts", { n: restarts }) : ""],
      ].map(([l, v, n]) => `<div class="card kpi"><div class="label">${esc(l)}</div><div class="value">${esc(v)}</div><div class="note">${esc(n)}</div></div>`).join("");
      $("#d-list").innerHTML = M.containerTable("docker", list, true);
      if (!M.guests().length) $("#d-list").innerHTML = `<div class="empty"><h2>${esc(t("docker.no_guests"))}</h2><p>${esc(t("docker.no_guests_text"))}</p><a class="btn" href="#/settings/agents">${esc(t("guest.install"))}</a></div>`;
    }
    const render = M.throttle(draw, 1000);
    $("#d-q").addEventListener("input", (e) => { q = e.target.value; render(true); });
    $("#d-st").addEventListener("change", (e) => { stf = e.target.value; render(true); });
    draw();
    return { onReport: () => render(), onStatus: () => render(), onAgent: () => render(), onSnapshot: () => render(true), render: () => render(true) };
  };

  // ---------- disks ----------
  M.pages.disks = function (view) {
    M.setTitle(t("disks.title"), t("disks.subtitle"));
    function rank(h) {
      const st = M.reports(h).storage;
      if (!st) return 3;
      if ((st.logical || []).some((l) => ["degraded", "failed", "rebuilding"].includes(l.state)) || (st.physical || []).some((p) => p.state === "failed" || p.state === "warning")) return 0;
      if ((st.controllers || []).some((c) => c.status !== "OK") || (st.errors || []).length) return 1;
      return 2;
    }
    function draw() {
      const hosts = M.hosts().slice().sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
      let total = 0, bad = 0, lds = 0, ldBad = 0;
      for (const h of hosts) {
        const st = M.reports(h).storage || {};
        total += (st.physical || []).length;
        bad += (st.physical || []).filter((p) => p.state === "failed" || p.state === "warning").length;
        lds += (st.logical || []).length;
        ldBad += (st.logical || []).filter((l) => l.state !== "ok" && l.state !== "unknown").length;
      }
      const kpis = `<div class="kpis">
        <div class="card kpi"><div class="label">${esc(t("disks.k_phys"))}</div><div class="value">${total - bad} <small>/ ${total}</small></div><div class="note ${bad ? "txt-crit" : ""}">${esc(bad ? t("disks.k_bad", { n: bad }) : t("disks.k_ok"))}</div></div>
        <div class="card kpi"><div class="label">${esc(t("disks.k_raid"))}</div><div class="value">${lds - ldBad} <small>/ ${lds}</small></div><div class="note ${ldBad ? "txt-crit" : ""}">${esc(ldBad ? t("disks.k_raid_bad", { n: ldBad }) : t("disks.k_ok"))}</div></div>
      </div>`;
      const cards = hosts.map((h) => {
        const st = M.reports(h).storage;
        const ph = (st && st.physical) || [];
        const rows = ph.slice().sort((a, b) => (a.state === "ok") - (b.state === "ok")).map((p) => {
          const [cls, label] = M.diskState(p);
          return { data: p, cells: [`<b>${esc(p.bay ? "bay " + p.bay : p.id)}</b><div class="dim mono">${esc(p.id)}</div>`, `${esc(p.model || "–")}<div class="dim mono">${esc(p.serial || "")}</div>`,
            esc(fmt.size(p.size)), `<span class="tag">${esc(p.type || "?")}</span>`, `<span class="${p.temp >= 55 ? "txt-warn" : ""}">${fmt.temp(p.temp)}</span>`,
            esc(p.smart || "–"), p.power_on_hours ? fmt.int(p.power_on_hours) : "–", M.statusHTML(cls, label + (p.status && p.status !== "OK" ? " (" + p.status + ")" : ""))] };
        });
        return `<div class="section card"><div class="card-head"><h2><a class="link-plain" href="#/server/${h.id}">${esc(h.name)}</a></h2>
            <span class="note">${st ? esc(t("src." + st.source)) + (st.ts ? " · " + esc(fmt.ago(st.ts)) : "") : esc(t("disks.no_report"))}</span></div>
          ${st && st.errors && st.errors.length ? `<div class="banner neutral small">${esc(st.errors.join("; "))}</div>` : ""}
          ${M.baysHTML(st, false)}
          ${ph.length ? M.table("disks-" + h.id, [{ label: t("disks.c_slot") }, { label: t("disks.c_model") }, { label: t("disks.c_size"), right: 1 }, { label: t("disks.c_type") },
            { label: t("disks.c_temp"), right: 1, sort: (p) => p.temp }, { label: "SMART" }, { label: t("disks.c_hours"), right: 1, sort: (p) => p.power_on_hours }, { label: t("disks.c_state") }], rows) : ""}
        </div>`;
      }).join("");
      view.innerHTML = kpis + (cards || M.empty(t("disks.none")));
    }
    const render = M.throttle(draw, 2000);
    draw();
    return { onReport: (id, kind) => { if (!kind || kind === "storage") render(); }, onAgent: () => render(), onSnapshot: () => render(true), render: () => render(true) };
  };

  // ---------- network map ----------
  M.pages.network = function (view) {
    M.setTitle(t("netmap.title"), t("netmap.subtitle"));
    view.innerHTML = `<div class="netmap-wrap card"><div class="netmap" id="netmap"></div></div>
      <div class="legend-line dim">${esc(t("netmap.legend"))}</div>
      <div class="card section" id="nm-detail" hidden></div>`;
    const expanded = new Set();
    let selected = null;
    const colW = 300, vmW = 240, vmH = 40, gap = 8;

    function vmIPs(host, vm) {
      const g = M.guestFor(host.id, vm.vmid);
      const ips = [];
      if (g) for (const i of g.info.ifaces || []) for (const a of i.addrs || []) if (!a.includes(":")) ips.push(a.split("/")[0]);
      if (!ips.length) for (const n of vm.nets || []) if (n.ip) ips.push(n.ip.split("/")[0]);
      return { ips, g };
    }

    function draw() {
      const hosts = M.hosts();
      if (!hosts.length) { $("#netmap").innerHTML = M.empty(t("netmap.none")); return; }
      const W = Math.max(hosts.length * colW + 40, 700);
      const parts = [];
      let maxY = 0;
      const netX = W / 2, netY = 34;
      parts.push(`<g class="nm-node nm-net" data-sel="net"><rect x="${netX - 120}" y="${netY - 22}" width="240" height="44" rx="10"/>
        <text x="${netX}" y="${netY - 2}" text-anchor="middle" class="nm-t1">${esc(t("netmap.physical"))}</text>
        <text x="${netX}" y="${netY + 14}" text-anchor="middle" class="nm-t2">${esc(t("netmap.switches"))}</text></g>`);
      hosts.forEach((h, i) => {
        const x0 = 20 + i * colW, cx = x0 + colW / 2 - 10;
        const r = M.reports(h);
        const s = h.latest || {};
        const p = M.pick(s);
        const traffic = M.online(h) ? (p["net.rx_bps"] || 0) + (p["net.tx_bps"] || 0) : 0;
        const sw = Math.max(1.5, Math.min(7, 1.5 + Math.log10(1 + traffic * 8 / 1e6) * 2));
        const sy = 120;
        parts.push(`<path class="nm-uplink ${M.online(h) ? "" : "off"}" stroke-width="${sw}" d="M${netX} ${netY + 22} C ${netX} ${netY + 60}, ${cx} ${sy - 70}, ${cx} ${sy - 26}"/>`);
        if (M.online(h)) parts.push(`<text class="nm-traffic" x="${cx + 8}" y="${sy - 40}">${esc(fmt.bits(traffic))}</text>`);
        const hc = M.health(h);
        parts.push(`<g class="nm-node nm-host ${hc}" data-sel="host:${h.id}"><rect x="${cx - 120}" y="${sy - 26}" width="240" height="52" rx="10"/>
          ${shape(hc, cx - 102, sy)}<text x="${cx - 88}" y="${sy - 4}" class="nm-t1">${esc(clip(h.name, 24))}</text>
          <text x="${cx - 88}" y="${sy + 13}" class="nm-t2">${esc(clip(((r.net && r.net.ifaces) || []).filter((x) => x.kind === "phys" && x.up).map((x) => x.name).join(", ") || t("health." + hc), 30))}</text></g>`);
        let y = sy + 50;
        const bridges = (r.net && r.net.bridges) || [];
        const vms = ((r.vms && r.vms.vms) || []).filter((v) => !v.template);
        const byBridge = new Map();
        for (const b of bridges) byBridge.set(b.name, []);
        for (const vm of vms) {
          const nets = vm.nets && vm.nets.length ? vm.nets : [{ bridge: "" }];
          const seen = new Set();
          for (const n of nets) {
            const bk = n.bridge || "";
            if (seen.has(bk + "|" + (n.tag || 0))) continue;
            seen.add(bk + "|" + (n.tag || 0));
            if (!byBridge.has(bk)) byBridge.set(bk, []);
            byBridge.get(bk).push({ vm, tag: n.tag || 0 });
          }
        }
        const trunkX = cx - 120 + 14;
        const trunkTop = sy + 26;
        let lastBridgeY = trunkTop;
        for (const [bname, items] of byBridge) {
          const b = bridges.find((x) => x.name === bname);
          const by = y + 22;
          parts.push(`<path class="nm-edge" d="M${trunkX} ${lastBridgeY} V ${by} H ${trunkX + 16}"/>`);
          lastBridgeY = by;
          const label = bname ? bname + (b && b.ports.length ? " · " + b.ports.join(", ") : "") : t("netmap.no_bridge");
          const sub = b ? [(b.addrs || []).join(", "), b.vlan_aware ? "VLAN-aware" : ""].filter(Boolean).join(" · ") : "";
          parts.push(`<g class="nm-node nm-bridge" data-sel="bridge:${h.id}:${esc(bname)}"><rect x="${trunkX + 16}" y="${by - 20}" width="${colW - 50}" height="40" rx="8"/>
            <text x="${trunkX + 28}" y="${by - 3}" class="nm-t1">${esc(clip(label, 30))}</text><text x="${trunkX + 28}" y="${by + 12}" class="nm-t2">${esc(clip(sub, 34))}</text></g>`);
          y = by + 28;
          const groups = new Map();
          for (const it of items) {
            if (!groups.has(it.tag)) groups.set(it.tag, []);
            groups.get(it.tag).push(it.vm);
          }
          const vx = trunkX + 40;
          const tags = [...groups.keys()].sort((a, b) => a - b);
          let lastVMY = by + 20;
          for (const tag of tags) {
            const list = groups.get(tag).sort((a, b) => (b.status === "running") - (a.status === "running") || a.vmid - b.vmid);
            if (tags.length > 1 || tag) {
              parts.push(`<text class="nm-vlan" x="${vx}" y="${y + 10}">${esc(tag ? "VLAN " + tag : t("netmap.untagged"))}</text>`);
              y += 18;
            }
            const gk = h.id + ":" + bname + ":" + tag;
            const limit = expanded.has(gk) ? list.length : 6;
            for (const vm of list.slice(0, limit)) {
              const vy = y + vmH / 2;
              const st = vm.status === "running" ? (M.guestFor(h.id, vm.vmid) ? M.health(M.guestFor(h.id, vm.vmid)) : "ok") : (M.vmMonitored(h.id, vm) ? "crit" : "off");
              const tr = vm.status === "running" ? (vm.net_rx_bps || 0) + (vm.net_tx_bps || 0) : 0;
              const ew = Math.max(1, Math.min(5, 1 + Math.log10(1 + tr * 8 / 1e6) * 1.6));
              const { ips, g } = vmIPs(h, vm);
              const dk = g && g.reports && g.reports.docker ? t("netmap.docker", { n: (g.reports.docker.containers || []).length }) : "";
              parts.push(`<path class="nm-edge vm ${vm.status === "running" ? "" : "off"}" stroke-width="${ew}" d="M${vx - 12} ${lastVMY} V ${vy} H ${vx}"/>`);
              lastVMY = vy;
              parts.push(`<g class="nm-node nm-vm ${st}" data-sel="vm:${h.id}:${vm.vmid}"><rect x="${vx}" y="${y}" width="${vmW - 20}" height="${vmH}" rx="8"/>
                ${shape(st, vx + 13, vy)}<text x="${vx + 26}" y="${vy - 3}" class="nm-t1">${esc(clip(vm.name, 18))}</text>
                <text x="${vx + 26}" y="${vy + 12}" class="nm-t2">${esc(clip([ips[0] || (vm.type === "lxc" ? "CT " : "VM ") + vm.vmid, dk].filter(Boolean).join(" · "), 28))}</text>
                ${tr ? `<text x="${vx + vmW - 26}" y="${vy - 3}" text-anchor="end" class="nm-t2">${esc(fmt.bits(tr))}</text>` : ""}</g>`);
              y += vmH + gap;
            }
            if (list.length > limit || expanded.has(gk) && list.length > 6) {
              const more = list.length > limit;
              parts.push(`<g class="nm-more" data-exp="${esc(gk)}"><text x="${vx + 4}" y="${y + 12}">${esc(more ? t("netmap.more", { n: list.length - limit }) : t("netmap.less"))}</text></g>`);
              y += 22;
            }
          }
          y += 10;
        }
        maxY = Math.max(maxY, y);
      });
      const H = Math.max(maxY + 20, 220);
      $("#netmap").innerHTML = `<svg viewBox="0 0 ${W} ${H}" width="${W}" height="${H}" role="img" aria-label="${esc(t("netmap.title"))}">${parts.join("")}</svg>`;
      if (selected) detail(selected);
    }

    function shape(kind, x, y) {
      if (kind === "warn") return `<rect class="sh warn" x="${x - 4.5}" y="${y - 4.5}" width="9" height="9" transform="rotate(45 ${x} ${y})"/>`;
      if (kind === "crit") return `<path class="sh crit" d="M${x} ${y - 6} L${x + 6} ${y + 5} L${x - 6} ${y + 5} Z"/>`;
      if (kind === "off" || kind === "new") return `<circle class="sh off" cx="${x}" cy="${y}" r="4.5"/>`;
      return `<circle class="sh ok" cx="${x}" cy="${y}" r="5"/>`;
    }
    function clip(s, n) { s = String(s || ""); return s.length > n ? s.slice(0, n - 1) + "…" : s; }

    function detail(sel) {
      const box = $("#nm-detail");
      const [kind, a, b] = sel.split(":");
      let html = "";
      if (kind === "host") {
        const h = M.state.agents.get(+a);
        if (!h) return;
        const ifs = ((M.reports(h).net || {}).ifaces || []);
        html = `<div class="card-head"><h2>${esc(h.name)}</h2><a class="link" href="#/server/${h.id}">${esc(t("netmap.open"))}</a></div>` +
          M.table("nm-if", [{ label: t("net.name") }, { label: t("net.kind") }, { label: "MAC" }, { label: t("net.addr") }, { label: t("net.speed") }],
            ifs.map((i) => ({ cells: [`<b>${esc(i.name)}</b>${i.master ? `<div class="dim">→ ${esc(i.master)}</div>` : ""}`, `<span class="tag">${esc(t("kind." + i.kind))}</span>`,
              `<span class="mono">${esc(i.mac)}</span>`, `<span class="mono">${esc((i.addrs || []).join(", ") || "–")}</span>`, i.speed ? i.speed + " Mbit/s" : (i.up ? "" : t("net.down"))] })));
      } else if (kind === "vm") {
        const h = M.state.agents.get(+a);
        const vm = h && ((M.reports(h).vms || {}).vms || []).find((v) => v.vmid === +b);
        if (!vm) return;
        const { ips } = vmIPs(h, vm);
        html = `<div class="card-head"><h2>${esc(vm.name)} <span class="dim">${vm.type === "lxc" ? "CT" : "VM"} ${vm.vmid}</span></h2><a class="link" href="#/vm/${h.id}/${vm.vmid}">${esc(t("netmap.open"))}</a></div>
          <div class="kv"><span>${esc(t("vm.server"))}</span><b>${esc(h.name)}</b><span>${esc(t("vm.status"))}</span><b>${esc(t("vm.st." + vm.status))}</b>
          <span>IP</span><b class="mono">${esc(ips.join(", ") || "–")}</b><span>${esc(t("vm.net"))}</span><b>↓ ${esc(fmt.bits(vm.net_rx_bps))} · ↑ ${esc(fmt.bits(vm.net_tx_bps))}</b></div>` +
          M.table("nm-vmn", [{ label: "" }, { label: "MAC" }, { label: "Bridge" }, { label: "VLAN" }], (vm.nets || []).map((n) => ({ cells: [esc(n.id), `<span class="mono">${esc(n.mac)}</span>`, esc(n.bridge || "–"), esc(n.tag || "–")] })));
      } else if (kind === "bridge") {
        const h = M.state.agents.get(+a);
        const br = ((M.reports(h).net || {}).bridges || []).find((x) => x.name === b);
        const vms = ((M.reports(h).vms || {}).vms || []).filter((v) => (v.nets || []).some((n) => n.bridge === b));
        html = `<div class="card-head"><h2>${esc(b || t("netmap.no_bridge"))} <span class="dim">${esc(h.name)}</span></h2></div>
          <div class="kv"><span>${esc(t("netmap.ports"))}</span><b>${esc(br ? br.ports.join(", ") || "–" : "–")}</b><span>IP</span><b class="mono">${esc(br ? (br.addrs || []).join(", ") || "–" : "–")}</b>
          <span>VLAN-aware</span><b>${esc(br && br.vlan_aware ? t("yes") : t("no"))}</b><span>VM/CT</span><b>${vms.length}</b></div>`;
      } else {
        html = `<div class="card-head"><h2>${esc(t("netmap.physical"))}</h2></div><p class="muted">${esc(t("netmap.physical_note"))}</p>`;
      }
      box.hidden = false;
      box.innerHTML = html;
    }

    $("#netmap").addEventListener("click", (e) => {
      const more = e.target.closest("[data-exp]");
      if (more) {
        const k = more.dataset.exp;
        if (expanded.has(k)) expanded.delete(k); else expanded.add(k);
        draw();
        return;
      }
      const n = e.target.closest("[data-sel]");
      if (n) {
        selected = n.dataset.sel;
        detail(selected);
        $("#nm-detail").scrollIntoView({ behavior: "smooth", block: "nearest" });
      }
    });
    const render = M.throttle(draw, 3000);
    draw();
    return { onReport: () => render(), onStatus: () => render(), onAgent: () => render(), onSnapshot: () => render(true), onTheme: () => render(true) };
  };

  // ---------- alerts ----------
  M.pages.alerts = function (view) {
    let tab = "active";
    let history = null;
    let hostFilter = "";
    M.setTitle(t("alerts.title"), t("alerts.subtitle"));
    view.innerHTML = `<div class="tabs" id="al-tabs"></div><div id="al-body"></div>`;
    function tabs() {
      $("#al-tabs").innerHTML = ["active", "history", "rules"].map((k) =>
        `<button type="button" class="${k === tab ? "active" : ""}" data-tab="${k}">${esc(t("alerts.tab_" + k))}${k === "active" && M.state.alerts.size ? ` <small class="badge">${M.state.alerts.size}</small>` : ""}</button>`).join("");
    }
    $("#al-tabs").addEventListener("click", (e) => {
      const b = e.target.closest("[data-tab]");
      if (!b) return;
      tab = b.dataset.tab;
      if (tab === "history") history = null;
      draw();
    });
    const agentName = (id) => (M.state.agents.get(id) || {}).name || "#" + id;

    function activeHTML() {
      const list = [...M.state.alerts.values()].sort((a, b) => (b.severity === "critical") - (a.severity === "critical") || b.started_at - a.started_at);
      if (!list.length) return `<div class="card empty"><h2>${esc(t("alerts.none"))}</h2><p class="muted">${esc(t("alerts.none_text"))}</p></div>`;
      return `<div class="card">${list.map((a) => {
        const sil = a.silenced_until > M.now();
        return `<div class="alert-row">${M.sevHTML(a.severity)}
          <div class="grow"><b>${esc(a.title)}</b><div class="dim">${esc(a.detail)}</div>
            <div class="dim small">${esc(agentName(a.agent_id))} · ${esc(t("alerts.since", { v: fmt.full(a.started_at), d: fmt.duration((M.now() - a.started_at) / 1000) }))}${sil ? " · " + esc(t("alerts.silenced_until", { v: fmt.date(a.silenced_until) })) : ""}</div></div>
          <div class="btn-group">${sil ? `<button class="btn small" data-sil="${a.id}" data-min="0">${esc(t("alerts.unsilence"))}</button>` :
            [60, 480, 1440].map((m) => `<button class="btn small" data-sil="${a.id}" data-min="${m}">${esc(t("alerts.silence_" + m))}</button>`).join("")}</div></div>`;
      }).join("")}</div>`;
    }

    async function historyHTML() {
      if (!history) {
        try { history = await M.api("/api/v1/alerts?state=history"); } catch (e) { return M.empty(e.message); }
      }
      const list = history.filter((a) => !hostFilter || String(a.agent_id) === hostFilter);
      const opts = [...new Set(history.map((a) => a.agent_id))].map((id) => `<option value="${id}" ${String(id) === hostFilter ? "selected" : ""}>${esc(agentName(id))}</option>`).join("");
      return `<div class="filters"><select id="al-host"><option value="">${esc(t("filter.all_servers"))}</option>${opts}</select><span class="dim">${esc(t("alerts.history_note", { n: list.length }))}</span></div>
        <div class="card">${M.table("al-hist", [
        { label: t("alerts.c_sev"), sort: (a) => a.severity }, { label: t("alerts.c_event") }, { label: t("alerts.c_server"), sort: (a) => agentName(a.agent_id) },
        { label: t("alerts.c_start"), sort: (a) => a.started_at }, { label: t("alerts.c_end"), sort: (a) => a.resolved_at || 9e15 }, { label: t("alerts.c_dur"), right: 1, sort: (a) => (a.resolved_at || M.now()) - a.started_at }],
      list.map((a) => ({ data: a, cells: [M.sevHTML(a.severity), `<b>${esc(a.title)}</b><div class="dim">${esc(a.detail)}</div>`, esc(agentName(a.agent_id)),
        esc(fmt.full(a.started_at)), a.resolved_at ? esc(fmt.full(a.resolved_at)) : `<span class="txt-crit">${esc(t("alerts.still"))}</span>`,
        esc(fmt.duration(((a.resolved_at || M.now()) - a.started_at) / 1000))] })), { empty: t("alerts.history_none") })}</div>`;
    }

    async function rulesHTML() {
      let s;
      try { s = await M.api("/api/v1/alert-settings"); } catch (e) { return M.empty(e.message); }
      const r = s.rules;
      const num = (k, step = 1) => `<input type="number" name="${k}" value="${esc(r[k])}" step="${step}" min="0">`;
      const chk = (k) => `<label class="switch"><input type="checkbox" name="${k}" ${r[k] ? "checked" : ""}><i></i></label>`;
      const row = (en, label, inputs, note) => `<div class="rule-row">${chk(en)}<div class="grow"><b>${esc(t(label))}</b>${note ? `<div class="dim small">${esc(t(note))}</div>` : ""}</div><div class="rule-in">${inputs}</div></div>`;
      return `<form class="card" id="rules-form">
        ${row("offline_enabled", "rule.offline", `<span class="dim">30 s</span>`, "rule.offline_note")}
        ${row("cpu_enabled", "rule.cpu", `${num("cpu_pct")} % · ${num("cpu_for_min")} ${esc(t("unit.min"))}`)}
        ${row("ram_enabled", "rule.ram", `${num("ram_pct")} % · ${num("ram_for_min")} ${esc(t("unit.min"))}`)}
        ${row("disk_enabled", "rule.disk", `${num("disk_warn")} % / ${num("disk_crit")} %`, "rule.disk_note")}
        ${row("raid_enabled", "rule.raid", "", "rule.raid_note")}
        ${row("phys_enabled", "rule.phys", "", "rule.phys_note")}
        ${row("disk_temp_enabled", "rule.disk_temp", `${num("disk_temp")} °C`)}
        ${row("vm_enabled", "rule.vm", "", "rule.vm_note")}
        ${row("container_enabled", "rule.container", "", "rule.container_note")}
        <div class="rule-row"><span></span><div class="grow"><b>${esc(t("rule.repeat"))}</b><div class="dim small">${esc(t("rule.repeat_note"))}</div></div><div class="rule-in">${num("repeat_min")} ${esc(t("unit.min"))}</div></div>
        <div class="form-foot"><span class="err" id="rules-err"></span><button class="btn primary" type="submit">${esc(t("btn.save"))}</button></div>
      </form>`;
    }

    async function draw() {
      tabs();
      const body = $("#al-body");
      if (tab === "active") body.innerHTML = activeHTML();
      else if (tab === "history") body.innerHTML = await historyHTML();
      else {
        body.innerHTML = await rulesHTML();
        const f = $("#rules-form");
        f.addEventListener("submit", async (e) => {
          e.preventDefault();
          const rules = {};
          for (const el of $$("input", f)) rules[el.name] = el.type === "checkbox" ? el.checked : Number(el.value);
          try {
            await M.api("/api/v1/alert-settings", { method: "PUT", body: { rules } });
            M.toast(t("msg.saved"), "ok");
            $("#rules-err").textContent = "";
          } catch (err) {
            $("#rules-err").textContent = err.message;
          }
        });
      }
      const hs = $("#al-host");
      if (hs) hs.addEventListener("change", (e) => { hostFilter = e.target.value; draw(); });
    }
    view.addEventListener("click", async (e) => {
      const b = e.target.closest("[data-sil]");
      if (!b) return;
      try {
        const a = await M.api(`/api/v1/alerts/${b.dataset.sil}/silence`, { method: "POST", body: { minutes: +b.dataset.min } });
        M.state.alerts.set(a.id, a);
        draw();
      } catch (err) { M.toast(err.message, "crit"); }
    });
    draw();
    const tick = setInterval(() => { if (tab === "active") draw(); }, 30000);
    return { onAlert: () => { if (tab === "active") draw(); else tabs(); }, onSnapshot: () => { if (tab === "active") draw(); }, render: () => { if (tab !== "rules") draw(); }, destroy: () => clearInterval(tick) };
  };
})();
