// Monitor.uz panel. Live data arrives over Server-Sent Events from the hub;
// the page never needs a manual reload. All texts come from /i18n/uz.json.
"use strict";

(function () {
  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

  // ---------- i18n ----------
  let TEXT = {};
  function t(key, params) {
    let s = TEXT[key];
    if (s == null) return key;
    if (params) for (const k in params) s = s.split("{" + k + "}").join(params[k]);
    return s;
  }
  function esc(v) {
    return String(v == null ? "" : v).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  }

  // ---------- formatting ----------
  const fmt = {
    pct(v, axis) {
      if (v == null || isNaN(v)) return "–";
      if (axis) return Math.round(v) + "%";
      return (v < 10 ? v.toFixed(1) : Math.round(v)) + "%";
    },
    bytes(v) {
      if (v == null || isNaN(v)) return "–";
      const u = ["B", "KB", "MB", "GB", "TB", "PB"];
      let i = 0;
      while (Math.abs(v) >= 1024 && i < u.length - 1) { v /= 1024; i++; }
      return (i === 0 || v >= 100 ? Math.round(v) : v.toFixed(1)) + " " + u[i];
    },
    bytesRate(v) { return v == null || isNaN(v) ? "–" : fmt.bytes(v) + "/s"; },
    // Network rates are reported in bytes/s and shown in bits/s.
    bits(v) {
      if (v == null || isNaN(v)) return "–";
      v *= 8;
      const u = ["bit/s", "Kbit/s", "Mbit/s", "Gbit/s"];
      let i = 0;
      while (Math.abs(v) >= 1000 && i < u.length - 1) { v /= 1000; i++; }
      return (i === 0 || v >= 100 ? Math.round(v) : v.toFixed(1)) + " " + u[i];
    },
    num(v) { return v == null || isNaN(v) ? "–" : (Math.abs(v) >= 100 ? Math.round(v) : v.toFixed(2)); },
    count(v) { return v == null || isNaN(v) ? "–" : (v >= 100 ? Math.round(v) : (Math.round(v * 10) / 10)); },
    ms(v) { return v == null || isNaN(v) ? "–" : (v < 10 ? v.toFixed(1) : Math.round(v)) + " " + t("unit.ms"); },
    duration(sec) {
      sec = Math.max(0, Math.floor(sec));
      const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
      if (d > 0) return t("dur.days", { d, h });
      if (h > 0) return t("dur.hours", { h, m });
      if (m > 0) return t("dur.mins", { m });
      return t("dur.secs", { s: sec });
    },
    ago(ms) {
      const s = Math.max(0, Math.round((serverNow() - ms) / 1000));
      if (s < 3) return t("time.now");
      if (s < 60) return t("time.sec_ago", { n: s });
      if (s < 3600) return t("time.min_ago", { n: Math.floor(s / 60) });
      if (s < 86400) return t("time.hour_ago", { n: Math.floor(s / 3600) });
      return t("time.day_ago", { n: Math.floor(s / 86400) });
    },
    p2: (n) => String(n).padStart(2, "0"),
    clock(ms, span) {
      const d = new Date(ms);
      const hm = fmt.p2(d.getHours()) + ":" + fmt.p2(d.getMinutes());
      if (span <= 15 * 60e3) return hm + ":" + fmt.p2(d.getSeconds());
      if (span <= 26 * 3600e3) return hm;
      return fmt.p2(d.getDate()) + "." + fmt.p2(d.getMonth() + 1) + " " + hm;
    },
    full(ms) {
      const d = new Date(ms);
      return fmt.p2(d.getDate()) + "." + fmt.p2(d.getMonth() + 1) + "." + d.getFullYear() + " " +
        fmt.p2(d.getHours()) + ":" + fmt.p2(d.getMinutes()) + ":" + fmt.p2(d.getSeconds());
    },
  };

  // ---------- state ----------
  const state = {
    hub: null,
    clockOffset: 0, // hub time - browser time
    agents: new Map(), // id -> AgentView
    spark: new Map(), // id -> recent cpu values (overview sparklines)
    conn: "connecting",
    page: null,
  };
  const SPARK_N = 120;
  function serverNow() { return Date.now() + state.clockOffset; }

  // Thresholds used only for bar colours (alert rules come in stage 5).
  function level(kind, v) {
    if (v == null) return "";
    if (kind === "disk") return v >= 95 ? "crit" : v >= 85 ? "warn" : "";
    return v >= 90 ? "warn" : "";
  }

  function statusHTML(status) {
    const map = { online: "ok", offline: "off", new: "off", revoked: "off" };
    return `<span class="status ${map[status] || "off"}"><i class="shape"></i>${esc(t("status." + status))}</span>`;
  }

  function maxFS(s) {
    let best = null;
    for (const f of (s && s.fs) || []) if (!best || f.used_pct > best.used_pct) best = f;
    return best;
  }

  // Values for the main charts, mirroring model.Sample.Flatten on the hub.
  function pick(s) {
    const o = {};
    if (s.cpu) { o["cpu.usage"] = s.cpu.usage; o["cpu.iowait"] = s.cpu.iowait; }
    if (s.mem) { o["mem.used_pct"] = s.mem.used_pct; o["mem.swap_pct"] = s.mem.swap_pct; }
    if (s.load) { o["load.1"] = s.load.l1; o["load.5"] = s.load.l5; o["load.15"] = s.load.l15; }
    if (s.net) {
      let rx = 0, tx = 0;
      for (const n of s.net) if (n.kind === "phys") { rx += n.rx_bps; tx += n.tx_bps; }
      o["net.rx_bps"] = rx; o["net.tx_bps"] = tx;
    }
    if (s.disks) {
      let r = 0, w = 0;
      for (const d of s.disks) if (!d.name.startsWith("md")) { r += d.read_bps; w += d.write_bps; }
      o["disk.read_bps"] = r; o["disk.write_bps"] = w;
    }
    if (s.psi) { o["psi.cpu_some"] = s.psi.cpu_some; o["psi.mem_some"] = s.psi.mem_some; o["psi.io_some"] = s.psi.io_some; }
    return o;
  }

  // Inline style attributes are blocked by the CSP; widths/colours are
  // applied through the DOM after rendering.
  function applyStyles(root) {
    for (const el of $$("[data-w]", root)) el.style.width = Math.max(0, Math.min(100, +el.dataset.w)) + "%";
    for (const el of $$("[data-bg]", root)) el.style.background = el.dataset.bg;
  }

  function bar(v, kind) {
    return `<div class="bar ${level(kind, v)}"><i data-w="${v == null ? 0 : v}"></i></div>`;
  }

  // ---------- API ----------
  async function api(path) {
    const r = await fetch(path, { headers: { Accept: "application/json" }, cache: "no-store" });
    if (!r.ok) throw new Error(path + ": " + r.status);
    return r.json();
  }
  async function history(id, keys, from, to, points) {
    const q = new URLSearchParams({ from, to, points });
    for (const k of keys) q.append("key", k);
    const r = await api(`/api/v1/agents/${id}/history?${q}`);
    return r.series;
  }

  // ---------- live stream ----------
  let es = null;
  function connect() {
    if (es) es.close();
    es = new EventSource("/api/v1/stream");
    es.addEventListener("snapshot", (e) => {
      const snap = JSON.parse(e.data);
      state.hub = snap.hub;
      state.clockOffset = snap.hub.now - Date.now();
      const reconnect = state.conn === "off";
      state.agents = new Map(snap.agents.map((a) => [a.id, a]));
      setConn("on");
      renderSidebar();
      if (reconnect || !state.page) route();
      else if (state.page.onSnapshot) state.page.onSnapshot();
    });
    es.addEventListener("sample", (e) => {
      const { id, s } = JSON.parse(e.data);
      const a = state.agents.get(id);
      if (!a) return;
      a.latest = s;
      a.last_seen = serverNow();
      if (a.status !== "online") a.status = "online";
      const sp = state.spark.get(id) || [];
      sp.push(s.cpu ? s.cpu.usage : null);
      if (sp.length > SPARK_N) sp.splice(0, sp.length - SPARK_N);
      state.spark.set(id, sp);
      if (state.page && state.page.onSample) state.page.onSample(id, s);
    });
    es.addEventListener("status", (e) => {
      const m = JSON.parse(e.data);
      const a = state.agents.get(m.id);
      if (!a) return;
      a.status = m.status;
      a.last_seen = m.last_seen;
      renderSidebar();
      if (state.page && state.page.onStatus) state.page.onStatus(m.id);
    });
    es.addEventListener("agent", (e) => {
      const v = JSON.parse(e.data);
      const old = state.agents.get(v.id);
      state.agents.set(v.id, Object.assign({}, old, v, { latest: v.latest || (old && old.latest) }));
      renderSidebar();
      if (state.page && state.page.onAgent) state.page.onAgent(v.id);
    });
    es.addEventListener("backfill", (e) => {
      const m = JSON.parse(e.data);
      if (state.page && state.page.onBackfill) state.page.onBackfill(m.id);
    });
    es.onerror = () => setConn("off"); // EventSource reconnects by itself
  }

  function setConn(c) {
    state.conn = c;
    const el = $("#live");
    el.classList.toggle("on", c === "on");
    el.classList.toggle("off", c === "off");
    $(".txt", el).textContent = t(c === "on" ? "live.on" : c === "off" ? "live.off" : "live.connecting");
  }

  // ---------- sidebar / chrome ----------
  const NAV = [
    { id: "overview", href: "#/", icon: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>' },
    { id: "servers", href: "#/servers", icon: '<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/>' },
    { id: "vms", stage: 2, icon: '<path d="M12 3l8 4.5v9L12 21l-8-4.5v-9z"/><path d="M12 12l8-4.5M12 12v9M12 12L4 7.5"/>' },
    { id: "docker", stage: 4, icon: '<path d="M12 3l9 5-9 5-9-5z"/><path d="M3 13l9 5 9-5"/>' },
    { id: "disks", stage: 3, icon: '<ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6"/><path d="M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/>' },
    { id: "network", stage: 2, icon: '<rect x="9" y="3" width="6" height="5" rx="1"/><rect x="3" y="16" width="6" height="5" rx="1"/><rect x="15" y="16" width="6" height="5" rx="1"/><path d="M12 8v4M6 16v-2h12v2"/>' },
    { id: "alerts", stage: 5, icon: '<path d="M6 16V11a6 6 0 1 1 12 0v5l1.5 2h-15z"/><path d="M10 20a2 2 0 0 0 4 0"/>' },
    { id: "settings", stage: 6, icon: '<path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12M20 18h0"/><circle cx="16" cy="6" r="2"/><circle cx="10" cy="12" r="2"/><circle cx="18" cy="18" r="2"/>' },
  ];

  function renderNav(active) {
    $("#nav").innerHTML = NAV.map((n) => {
      const icon = `<svg viewBox="0 0 24 24" aria-hidden="true">${n.icon}</svg>`;
      if (n.stage) return `<span class="nav-soon" title="${esc(t("soon.title", { n: n.stage }))}">${icon}${esc(t("nav." + n.id))}<small>${esc(t("nav.stage", { n: n.stage }))}</small></span>`;
      return `<a href="${n.href}" class="${n.id === active ? "active" : ""}">${icon}${esc(t("nav." + n.id))}</a>`;
    }).join("");
  }

  function renderSidebar() {
    const hosts = [...state.agents.values()].filter((a) => a.mode === "host").length;
    $("#side-foot").innerHTML = `${esc(t("sidebar.hub", { v: state.hub ? state.hub.version : "–" }))}<br>${esc(t("sidebar.agents", { n: hosts }))}`;
  }

  function setTitle(title, sub) {
    $("#page-title").textContent = title;
    $("#page-sub").textContent = sub || "";
    document.title = title + " · Monitor.uz";
  }

  // ---------- pages ----------
  function sortedAgents() {
    return [...state.agents.values()].filter((a) => !a.revoked).sort((a, b) => a.name.localeCompare(b.name));
  }

  function OverviewPage(view) {
    let timer = null;
    let lastRender = 0;

    async function primeSparks() {
      const now = serverNow();
      await Promise.all(sortedAgents().map(async (a) => {
        if (state.spark.has(a.id)) return;
        try {
          const h = await history(a.id, ["cpu.usage"], now - SPARK_N * 1000, now, SPARK_N);
          state.spark.set(a.id, h["cpu.usage"].map((p) => p[1]));
        } catch (_) { /* sparkline stays empty */ }
      }));
      schedule(true);
    }

    function render() {
      lastRender = Date.now();
      const list = sortedAgents();
      setTitle(t("overview.title"), t("overview.subtitle"));
      if (!list.length) {
        view.innerHTML = `<div class="card empty"><h2>${esc(t("overview.empty_title"))}</h2>
          <p>${esc(t("overview.empty_text"))}</p><code class="cmd">${esc(t("overview.empty_cmd"))}</code></div>`;
        return;
      }
      const online = list.filter((a) => a.status === "online");
      const avg = (f) => {
        const v = online.map((a) => a.latest && f(a.latest)).filter((x) => x != null);
        return v.length ? v.reduce((p, c) => p + c, 0) / v.length : null;
      };
      const cpu = avg((s) => s.cpu && s.cpu.usage), ram = avg((s) => s.mem && s.mem.used_pct);
      let worst = null;
      for (const a of online) {
        const f = maxFS(a.latest);
        if (f && (!worst || f.used_pct > worst.f.used_pct)) worst = { a, f };
      }
      const offline = list.length - online.length;
      const kpis = `
        <div class="kpis">
          <div class="card kpi"><div class="label">${esc(t("overview.servers"))}</div>
            <div class="value">${online.length} <small>/ ${list.length}</small></div>
            <div class="note">${offline ? statusHTML("offline") + " " + esc(t("overview.servers_note_some", { n: offline })) : statusHTML("online") + " " + esc(t("overview.servers_note_all"))}</div></div>
          <div class="card kpi"><div class="label">${esc(t("overview.avg_cpu"))}</div>
            <div class="value">${fmt.pct(cpu)}</div><div class="note">${esc(t("overview.across", { n: online.length }))}</div></div>
          <div class="card kpi"><div class="label">${esc(t("overview.avg_ram"))}</div>
            <div class="value">${fmt.pct(ram)}</div><div class="note">${esc(t("overview.across", { n: online.length }))}</div></div>
          <div class="card kpi"><div class="label">${esc(t("overview.max_disk"))}</div>
            <div class="value">${worst ? fmt.pct(worst.f.used_pct) : "–"}</div>
            <div class="note">${worst ? esc(t("overview.max_disk_note", { host: worst.a.name, mount: worst.f.mount })) : ""}</div></div>
        </div>`;
      const cards = list.map((a) => {
        const s = a.latest || {};
        const on = a.status === "online";
        const c = on && s.cpu ? s.cpu.usage : null;
        const m = on && s.mem ? s.mem.used_pct : null;
        const f = on ? maxFS(s) : null;
        const sub = [a.info && a.info.os, a.hostname !== a.name ? a.hostname : ""].filter(Boolean).join(" · ");
        const foot = on
          ? `<span>${esc(t("card.uptime", { v: fmt.duration(s.uptime || 0) }))}</span><span>${s.load ? esc(t("card.load", { v: fmt.num(s.load.l1) })) : ""}</span>`
          : `<span>${esc(t("card.seen", { v: a.last_seen ? fmt.ago(a.last_seen) : "–" }))}</span>`;
        return `<a class="card srv ${on ? "" : "is-off"}" href="#/server/${a.id}">
          <div class="srv-head"><div><div class="srv-name">${esc(a.name)}</div><div class="srv-sub">${esc(sub)}</div></div>${statusHTML(a.status)}</div>
          <div class="bars">
            <div class="bar-row"><span>${esc(t("card.cpu"))}</span>${bar(c, "cpu")}<span class="v">${fmt.pct(c)}</span></div>
            <div class="bar-row"><span>${esc(t("card.ram"))}</span>${bar(m, "ram")}<span class="v">${fmt.pct(m)}</span></div>
            <div class="bar-row"><span>${esc(t("card.disk"))}</span>${bar(f && f.used_pct, "disk")}<span class="v">${fmt.pct(f && f.used_pct)}</span></div>
          </div>
          <canvas class="spark" data-id="${a.id}"></canvas>
          <div class="srv-foot">${foot}</div></a>`;
      }).join("");
      view.innerHTML = kpis + `<div class="section"><h2 class="section-title">${esc(t("nav.servers"))}</h2><div class="servers">${cards}</div></div>`;
      applyStyles(view);
      for (const cv of $$("canvas.spark", view)) MChart.sparkline(cv, state.spark.get(+cv.dataset.id) || [], "--accent", 100);
    }

    // At most one full render per second, however many agents report.
    function schedule(now) {
      if (timer) return;
      const wait = now ? 0 : Math.max(0, 1000 - (Date.now() - lastRender));
      timer = setTimeout(() => { timer = null; render(); }, wait);
    }

    render();
    primeSparks();
    const tick = setInterval(() => schedule(), 5000); // refresh "x seconds ago"
    return {
      onSample: () => schedule(),
      onStatus: () => schedule(),
      onAgent: () => { primeSparks(); schedule(); },
      onSnapshot: () => schedule(true),
      destroy: () => { clearInterval(tick); clearTimeout(timer); },
    };
  }

  const RANGES = {
    live: { span: 5 * 60e3, points: 300, label: "range.live" },
    "1h": { span: 3600e3, points: 720, label: "range.1h" },
    "24h": { span: 24 * 3600e3, points: 720, label: "range.24h" },
    "7d": { span: 7 * 24 * 3600e3, points: 1008, label: "range.7d" },
  };

  const CHARTS = [
    { id: "cpu", title: "chart.cpu", max: 100, f: (v, ax) => fmt.pct(v, ax), series: [["cpu.usage", "s.cpu_usage", "--s1"], ["cpu.iowait", "s.cpu_iowait", "--s2"]] },
    { id: "mem", title: "chart.mem", max: 100, f: (v, ax) => fmt.pct(v, ax), series: [["mem.used_pct", "s.mem_used", "--s1"], ["mem.swap_pct", "s.mem_swap", "--s3"]] },
    { id: "net", title: "chart.net", max: null, f: (v) => fmt.bits(v), series: [["net.rx_bps", "s.net_rx", "--s1"], ["net.tx_bps", "s.net_tx", "--s2"]] },
    { id: "disk", title: "chart.disk", max: null, f: (v) => fmt.bytesRate(v), series: [["disk.read_bps", "s.disk_read", "--s1"], ["disk.write_bps", "s.disk_write", "--s2"]] },
    { id: "psi", title: "chart.psi", max: null, f: (v, ax) => fmt.pct(v, ax), series: [["psi.cpu_some", "s.psi_cpu", "--s1"], ["psi.mem_some", "s.psi_mem", "--s3"], ["psi.io_some", "s.psi_io", "--s2"]] },
    { id: "load", title: "chart.load", max: null, f: (v) => fmt.num(v), series: [["load.1", "s.load1", "--s1"], ["load.5", "s.load5", "--s2"], ["load.15", "s.load15", "--s4"]] },
  ];
  const ALL_KEYS = CHARTS.flatMap((c) => c.series.map((s) => s[0]));

  let savedRange = "live";
  try { savedRange = localStorage.getItem("range") || "live"; } catch (_) { /* private mode */ }
  if (!RANGES[savedRange]) savedRange = "live";

  function ServerPage(view, id) {
    let range = savedRange;
    let data = {}; // key -> [[ts, v]]
    let loading = false;
    let reloadTimer = null;
    let destroyed = false;
    const charts = {};

    const a0 = state.agents.get(id);
    if (!a0) {
      setTitle(t("server.not_found"));
      view.innerHTML = `<div class="card empty"><h2>${esc(t("server.not_found"))}</h2><p><a class="crumb" href="#/">← ${esc(t("nav.overview"))}</a></p></div>`;
      return {};
    }

    view.innerHTML = `
      <div id="srv-banner"></div>
      <div class="kpis k7" id="kpis"></div>
      <div class="toolbar"><h2>${esc(t("chart.title"))}</h2><div id="range-slot"></div></div>
      <div><div class="charts">${CHARTS.map((c) => `
        <div class="card chart-card"><div class="card-head"><h2>${esc(t(c.title))}</h2></div>
          <div class="legend" id="lg-${c.id}"></div><div class="chart-wrap" id="ch-${c.id}"></div></div>`).join("")}
      </div></div>
      <div class="section card"><div class="card-head"><h2>${esc(t("cores.title"))}</h2><span class="note" id="cores-note"></span></div><div class="cores" id="cores"></div></div>
      <div class="section card"><div class="card-head"><h2>${esc(t("net.title"))}</h2></div><div class="table-wrap" id="net-table"></div></div>
      <div class="section grid-2">
        <div class="card"><div class="card-head"><h2>${esc(t("disk.title"))}</h2></div><div class="table-wrap" id="disk-table"></div></div>
        <div class="card"><div class="card-head"><h2>${esc(t("fs.title"))}</h2></div><div class="table-wrap" id="fs-table"></div></div>
      </div>`;

    // Title area with range selector
    function renderHeader() {
      const a = state.agents.get(id);
      const inf = a.info || {};
      const s = a.latest || {};
      $("#page-title").innerHTML = `<a class="crumb" href="#/">← ${esc(t("server.back"))}</a>
        <span class="title-row"><span>${esc(a.name)}</span>${statusHTML(a.status)}</span>`;
      document.title = a.name + " · Monitor.uz";
      const meta = [
        inf.os, inf.kernel, inf.cpu_model,
        inf.cpu_cores ? t("server.meta_cores", { n: inf.cpu_cores }) : "",
        inf.mem_total ? fmt.bytes(inf.mem_total) + " RAM" : "",
        a.status === "online" && s.uptime ? t("server.uptime", { v: fmt.duration(s.uptime) }) : "",
        inf.version ? t("server.agent", { v: inf.version }) : "",
      ].filter(Boolean);
      $("#page-sub").innerHTML = `<div class="meta">${meta.map((m) => `<span>${esc(m)}</span>`).join("")}</div>`;
      let banner = "";
      if (a.status === "offline") banner = `<div class="banner">${statusHTML("offline")}<div>${esc(t("server.offline_banner", { v: a.last_seen ? fmt.ago(a.last_seen) : "–" }))}</div></div>`;
      else if (a.status === "new") banner = `<div class="banner neutral">${esc(t("server.new_banner"))}</div>`;
      if (s.na && s.na.length && a.status === "online") banner += `<div class="banner neutral">${esc(t("server.na", { v: s.na.join(", ") }))}</div>`;
      $("#srv-banner").innerHTML = banner;
    }

    function renderRangeSeg() {
      const seg = document.createElement("div");
      seg.className = "seg";
      seg.id = "range-seg";
      for (const k of Object.keys(RANGES)) {
        const b = document.createElement("button");
        b.type = "button";
        b.textContent = t(RANGES[k].label);
        b.className = k === range ? "active" : "";
        b.addEventListener("click", () => setRange(k));
        seg.append(b);
      }
      $("#range-slot").replaceChildren(seg);
    }

    function renderKPIs() {
      const a = state.agents.get(id);
      const s = a.status === "online" ? a.latest || {} : {};
      const m = s.mem;
      const p = pick(s);
      const ramNote = m ? (m.arc_max ? t("kpi.arc_note", { v: fmt.bytes(m.arc_size) }) : t("kpi.ram_note", { used: fmt.bytes(m.used), total: fmt.bytes(m.total) })) : "";
      const items = [
        [t("kpi.cpu"), fmt.pct(s.cpu && s.cpu.usage), s.cpu ? t("kpi.cpu_note", { v: fmt.pct(s.cpu.iowait) }) : ""],
        [t("kpi.ram"), fmt.pct(m && m.used_pct), ramNote],
        [t("kpi.swap"), m ? (m.swap_total ? fmt.pct(m.swap_pct) : t("kpi.swap_none")) : "–", m && m.swap_total ? fmt.bytes(m.swap_used) + " / " + fmt.bytes(m.swap_total) : ""],
        [t("kpi.load"), s.load ? fmt.num(s.load.l1) : "–", s.load ? fmt.num(s.load.l5) + " · " + fmt.num(s.load.l15) : t("kpi.load_note")],
        [t("kpi.net"), fmt.bits(p["net.rx_bps"]), p["net.tx_bps"] != null ? "↑ " + fmt.bits(p["net.tx_bps"]) : t("kpi.net_note")],
        [t("kpi.disk"), fmt.bytesRate(p["disk.read_bps"]), p["disk.write_bps"] != null ? t("s.disk_write") + " " + fmt.bytesRate(p["disk.write_bps"]) : t("kpi.disk_note")],
        [t("kpi.psi"), s.psi ? fmt.pct(Math.max(s.psi.cpu_some, s.psi.mem_some, s.psi.io_some)) : "–", s.psi ? [s.psi.cpu_some, s.psi.mem_some, s.psi.io_some].map((v) => fmt.pct(v)).join(" · ") : t("kpi.psi_note")],
      ];
      $("#kpis").innerHTML = items.map(([l, v, n]) => `<div class="card kpi"><div class="label">${esc(l)}</div><div class="value">${esc(v)}</div><div class="note">${esc(n)}</div></div>`).join("");
    }

    function renderCores() {
      const a = state.agents.get(id);
      const cores = (a.status === "online" && a.latest && a.latest.cpu && a.latest.cpu.cores) || [];
      $("#cores-note").textContent = cores.length ? t("cores.note", { n: cores.length }) : "";
      const box = $("#cores");
      if (box.children.length !== cores.length) {
        box.innerHTML = cores.map(() => `<div class="core"></div>`).join("") || `<span class="dim">${esc(t("empty.none"))}</span>`;
      }
      cores.forEach((v, i) => {
        const el = box.children[i];
        el.textContent = Math.round(v);
        el.title = t("cores.cell", { i, v: fmt.pct(v) });
        el.style.background = `color-mix(in srgb, var(--${v >= 90 ? "warn" : "accent"}) ${Math.round(8 + v * 0.75)}%, var(--track))`;
      });
    }

    function table(cols, rows) {
      if (!rows.length) return `<div class="dim">${esc(t("empty.none"))}</div>`;
      return `<table><thead><tr>${cols.map(([l, r]) => `<th class="${r ? "r" : ""}">${esc(l)}</th>`).join("")}</tr></thead>
        <tbody>${rows.map((r) => `<tr>${r.map((c, i) => `<td class="${cols[i][1] ? "r" : ""}">${c}</td>`).join("")}</tr>`).join("")}</tbody></table>`;
    }

    function renderTables() {
      const a = state.agents.get(id);
      const s = a.status === "online" ? a.latest || {} : (a.latest || {});
      const live = a.status === "online";
      const order = { phys: 0, bond: 1, bridge: 2, vlan: 3, other: 4 };
      const nets = (s.net || []).slice().sort((x, y) => (order[x.kind] - order[y.kind]) || x.name.localeCompare(y.name));
      $("#net-table").innerHTML = table(
        [[t("net.name")], [t("net.kind")], [t("net.rx"), 1], [t("net.tx"), 1], [t("net.pps"), 1], [t("net.errs"), 1], [t("net.drops"), 1]],
        live ? nets.map((n) => [
          `<b>${esc(n.name)}</b>`, `<span class="tag">${esc(t("kind." + n.kind))}</span>`,
          fmt.bits(n.rx_bps), fmt.bits(n.tx_bps), fmt.count(n.rx_pps) + " / " + fmt.count(n.tx_pps),
          n.errs > 0 ? `<span class="status warn"><i class="shape"></i>${fmt.count(n.errs)}</span>` : "0",
          n.drops > 0 ? fmt.count(n.drops) : "0",
        ]) : []);
      $("#disk-table").innerHTML = table(
        [[t("disk.name")], [t("disk.read"), 1], [t("disk.write"), 1], [t("disk.iops"), 1], [t("disk.util"), 1], [t("disk.await"), 1]],
        live ? (s.disks || []).map((d) => [
          `<b>${esc(d.name)}</b>`, fmt.bytesRate(d.read_bps), fmt.bytesRate(d.write_bps),
          fmt.count(d.read_iops) + " / " + fmt.count(d.write_iops), fmt.pct(d.util), fmt.ms(d.await_ms),
        ]) : []);
      $("#fs-table").innerHTML = table(
        [[t("fs.mount")], [t("fs.type")], [t("fs.size"), 1], [t("fs.used"), 1]],
        (s.fs || []).map((f) => [
          `<b>${esc(f.mount)}</b><div class="dim mono">${esc(f.device)}</div>`, `<span class="tag">${esc(f.type)}</span>`,
          fmt.bytes(f.total), `${bar(f.used_pct, "disk")}<span class="num">${fmt.pct(f.used_pct)}</span>`,
        ]));
      applyStyles($("#fs-table"));
    }

    function renderLegends() {
      for (const c of CHARTS) {
        $("#lg-" + c.id).innerHTML = c.series.map(([k, label, color]) => {
          const pts = data[k] || [];
          const last = pts.length ? pts[pts.length - 1][1] : null;
          return `<span><i data-bg="var(${color})"></i>${esc(t(label))}<b>${esc(c.f(last))}</b></span>`;
        }).join("");
        applyStyles($("#lg-" + c.id));
      }
    }

    function windowRange() {
      const now = serverNow();
      return { from: now - RANGES[range].span, to: now };
    }

    function drawCharts(message) {
      const r = windowRange();
      const gap = (span) => Math.max(5000, (span / RANGES[range].points) * 3);
      for (const c of CHARTS) {
        if (!charts[c.id]) {
          charts[c.id] = new MChart.LineChart($("#ch-" + c.id), {
            series: c.series.map(([key, label, color]) => ({ key, label: t(label), color })),
            max: c.max, format: c.f, emptyText: t("chart.no_data"), gapMs: gap,
            timeFormat: (ms, span) => fmt.clock(ms, span), tipTimeFormat: (ms) => fmt.full(ms),
          });
        }
        const sub = {};
        for (const [k] of c.series) sub[k] = data[k] || [];
        charts[c.id].setData(sub, r, message);
      }
      renderLegends();
    }

    async function load() {
      if (loading) return;
      loading = true;
      drawCharts(t("chart.loading"));
      const { from, to } = windowRange();
      try {
        const h = await history(id, ALL_KEYS, from, to, RANGES[range].points);
        if (destroyed) return;
        data = h;
      } catch (e) {
        console.error(e);
      } finally {
        loading = false;
      }
      drawCharts();
    }

    function setRange(k) {
      range = k;
      savedRange = k;
      try { localStorage.setItem("range", k); } catch (_) { /* ignore */ }
      renderRangeSeg();
      load();
    }

    function append(s) {
      const p = pick(s);
      const cut = serverNow() - RANGES[range].span;
      for (const k of ALL_KEYS) {
        if (p[k] == null) continue;
        const arr = data[k] || (data[k] = []);
        if (arr.length && arr[arr.length - 1][0] >= s.ts) continue;
        arr.push([s.ts, p[k]]);
        let i = 0;
        while (i < arr.length && arr[i][0] < cut) i++;
        if (i) arr.splice(0, i);
      }
    }

    renderHeader();
    renderRangeSeg();
    renderKPIs();
    renderCores();
    renderTables();
    load();

    // Long ranges are re-queried periodically; short ones get live points.
    const periodic = setInterval(() => {
      if (range === "24h" || range === "7d") load();
      else if (state.agents.get(id).status !== "online") drawCharts();
      renderHeader();
    }, 30000);

    return {
      onSample(aid, s) {
        if (aid !== id) return;
        if (range === "live" || range === "1h") { append(s); drawCharts(); }
        renderKPIs(); renderCores(); renderTables();
        if (state.agents.get(id).status !== "online") renderHeader();
      },
      onStatus(aid) { if (aid === id) { renderHeader(); renderKPIs(); renderCores(); renderTables(); } },
      onAgent(aid) { if (aid === id) renderHeader(); },
      onSnapshot() { renderHeader(); load(); },
      onBackfill(aid) {
        if (aid !== id) return;
        clearTimeout(reloadTimer);
        reloadTimer = setTimeout(load, 1500);
      },
      destroy() {
        destroyed = true;
        clearInterval(periodic);
        clearTimeout(reloadTimer);
        for (const k in charts) charts[k].destroy();
      },
    };
  }

  // ---------- router ----------
  function route() {
    if (state.page && state.page.destroy) state.page.destroy();
    state.page = null;
    const view = $("#view");
    const h = location.hash || "#/";
    $(".app").classList.remove("menu-open");
    let m;
    if ((m = /^#\/server\/(\d+)$/.exec(h))) {
      renderNav("servers");
      state.page = ServerPage(view, +m[1]);
    } else {
      renderNav(h === "#/servers" ? "servers" : "overview");
      state.page = OverviewPage(view);
    }
    window.scrollTo(0, 0);
  }

  // ---------- theme ----------
  function applyTheme(theme) {
    document.documentElement.dataset.theme = theme;
    const btn = $("#theme-btn");
    btn.title = t(theme === "dark" ? "theme.light" : "theme.dark");
    btn.setAttribute("aria-label", t("theme.toggle"));
    btn.innerHTML = theme === "dark"
      ? '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>'
      : '<svg viewBox="0 0 24 24"><path d="M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z"/></svg>';
    // Canvas colours come from CSS variables: redraw.
    if (state.page && state.page.onSnapshot) state.page.onSnapshot();
  }
  function initTheme() {
    let theme = null;
    try { theme = localStorage.getItem("theme"); } catch (_) { /* ignore */ }
    if (theme !== "light" && theme !== "dark") theme = window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
    applyTheme(theme);
    $("#theme-btn").addEventListener("click", () => {
      const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
      try { localStorage.setItem("theme", next); } catch (_) { /* ignore */ }
      applyTheme(next);
    });
  }

  // ---------- boot ----------
  async function boot() {
    try {
      TEXT = await api("/i18n/uz.json");
    } catch (e) {
      console.error("texts", e);
    }
    for (const el of $$("[data-i18n]")) el.textContent = t(el.dataset.i18n);
    for (const el of $$("[data-i18n-title]")) el.title = t(el.dataset.i18nTitle);
    initTheme();
    setConn("connecting");
    $("#menu-btn").addEventListener("click", () => $(".app").classList.toggle("menu-open"));
    $(".scrim").addEventListener("click", () => $(".app").classList.remove("menu-open"));
    window.addEventListener("hashchange", route);
    renderNav("overview");
    connect();
  }
  boot();
})();
