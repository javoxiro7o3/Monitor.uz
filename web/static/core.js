// Monitor.uz panel core: texts, formatting, API, live stream, router,
// shell (navigation, theme, toasts, notifications). Pages register
// themselves in M.pages. No external libraries; works offline.
"use strict";

window.M = (function () {
  const M = {};
  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
  M.$ = $;
  M.$$ = $$;

  // ---------- i18n ----------
  let TEXT = {};
  M.t = function (key, params) {
    let s = TEXT[key];
    if (s == null) return key;
    if (params) for (const k in params) s = s.split("{" + k + "}").join(params[k]);
    return s;
  };
  const t = M.t;
  M.esc = function (v) {
    return String(v == null ? "" : v).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  };
  const esc = M.esc;

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
    // Disk sizes as vendors print them (decimal: 600 GB, 1.8 TB).
    size(v) {
      if (!v) return "–";
      const u = ["B", "KB", "MB", "GB", "TB", "PB"];
      let i = 0;
      while (v >= 1000 && i < u.length - 1) { v /= 1000; i++; }
      return (v >= 100 || Number.isInteger(v) ? Math.round(v) : v.toFixed(1)) + " " + u[i];
    },
    bytesRate(v) { return v == null || isNaN(v) ? "–" : fmt.bytes(v) + "/s"; },
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
    int(v) { return v == null || isNaN(v) ? "–" : String(Math.round(v)).replace(/\B(?=(\d{3})+(?!\d))/g, " "); },
    ms(v) { return v == null || isNaN(v) ? "–" : (v < 10 ? v.toFixed(1) : Math.round(v)) + " " + t("unit.ms"); },
    temp(v) { return v == null || isNaN(v) || v === 0 ? "–" : Math.round(v) + " °C"; },
    duration(sec) {
      sec = Math.max(0, Math.floor(sec));
      const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
      if (d > 0) return t("dur.days", { d, h });
      if (h > 0) return t("dur.hours", { h, m });
      if (m > 0) return t("dur.mins", { m });
      return t("dur.secs", { s: sec });
    },
    ago(ms) {
      const s = Math.max(0, Math.round((M.now() - ms) / 1000));
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
    date(ms) {
      const d = new Date(ms);
      return fmt.p2(d.getDate()) + "." + fmt.p2(d.getMonth() + 1) + " " + fmt.p2(d.getHours()) + ":" + fmt.p2(d.getMinutes());
    },
  };
  M.fmt = fmt;

  // ---------- state ----------
  const state = {
    hub: null,
    clockOffset: 0,
    agents: new Map(), // id -> AgentView
    alerts: new Map(), // id -> active alert
    spark: new Map(), // id -> recent cpu values
    overrides: { vms: {}, containers: {} },
    conn: "connecting",
    page: null,
    user: null,
  };
  M.state = state;
  M.now = () => Date.now() + state.clockOffset;
  M.SPARK_N = 120;

  // ---------- data helpers ----------
  M.hosts = () => [...state.agents.values()].filter((a) => a.mode === "host" && !a.revoked).sort((a, b) => a.name.localeCompare(b.name));
  M.guests = () => [...state.agents.values()].filter((a) => a.mode === "guest" && !a.revoked).sort((a, b) => a.name.localeCompare(b.name));
  M.online = (a) => a && a.status === "online";
  M.reports = (a) => (a && a.reports) || {};
  M.allVMs = function () {
    const out = [];
    for (const h of M.hosts()) for (const vm of (M.reports(h).vms && M.reports(h).vms.vms) || []) out.push({ host: h, vm });
    return out;
  };
  M.guestFor = function (hostId, vmid) {
    for (const g of M.guests()) if (g.link && g.link.host_id === hostId && g.link.vmid === vmid) return g;
    return null;
  };
  M.containers = function () {
    const out = [];
    for (const g of M.guests()) for (const c of (M.reports(g).docker && M.reports(g).docker.containers) || []) out.push({ guest: g, c });
    return out;
  };
  M.vmMonitored = function (hostId, vm) {
    const k = hostId + ":" + vm.vmid;
    const o = state.overrides.vms || {};
    if (k in o) return o[k];
    return !!vm.onboot && !vm.template;
  };
  M.ctrMonitored = function (guestId, name) {
    const k = guestId + ":" + name;
    const o = state.overrides.containers || {};
    return k in o ? o[k] : true;
  };
  M.maxFS = function (s) {
    let best = null;
    for (const f of (s && s.fs) || []) if (!best || f.used_pct > best.used_pct) best = f;
    return best;
  };
  // Values for the main charts, mirroring model.Sample.Flatten on the hub.
  M.pick = function (s) {
    const o = {};
    if (!s) return o;
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
  };
  // Guest-side net total (all non-virtual interfaces).
  M.guestNet = function (s) {
    let rx = 0, tx = 0;
    for (const n of (s && s.net) || []) if (n.kind !== "bridge" && n.kind !== "other") { rx += n.rx_bps; tx += n.tx_bps; }
    return { rx, tx };
  };

  // Server health: connection first, then worst active alert.
  M.health = function (a) {
    if (!a) return "off";
    if (a.status === "new") return "new";
    if (a.status !== "online") return "off";
    if (a.health === "critical") return "crit";
    if (a.health === "warning") return "warn";
    return "ok";
  };
  M.statusHTML = function (kind, label) {
    const map = { ok: "ok", warn: "warn", crit: "crit", off: "off", new: "off", online: "ok", offline: "off", revoked: "off", running: "ok", stopped: "off" };
    const text = label != null ? label : t("health." + kind);
    return `<span class="status ${map[kind] || "off"}"><i class="shape"></i>${esc(text)}</span>`;
  };
  M.sevHTML = function (sev) {
    return M.statusHTML(sev === "critical" ? "crit" : "warn", t("sev." + sev));
  };
  M.level = function (kind, v) {
    if (v == null) return "";
    if (kind === "disk") return v >= 95 ? "crit" : v >= 85 ? "warn" : "";
    return v >= 90 ? "warn" : "";
  };
  M.bar = function (v, kind) {
    return `<div class="bar ${M.level(kind, v)}"><i data-w="${v == null ? 0 : v}"></i></div>`;
  };
  // Inline style attributes are blocked by the CSP; widths/colours are
  // applied through the DOM after rendering.
  M.applyStyles = function (root) {
    for (const el of $$("[data-w]", root)) el.style.width = Math.max(0, Math.min(100, +el.dataset.w)) + "%";
    for (const el of $$("[data-bg]", root)) el.style.background = el.dataset.bg;
  };
  M.empty = (text) => `<div class="dim empty-row">${esc(text || t("empty.none"))}</div>`;

  /**
   * Sortable table. cols: [{label, right, sort: row => value}], rows: [{cells: [html], data}]
   * Sorting state is kept per table id.
   */
  const sortState = {};
  M.table = function (id, cols, rows, opts = {}) {
    if (!rows.length) return M.empty(opts.empty);
    const st = sortState[id] || opts.defaultSort || null;
    if (st && cols[st.col] && cols[st.col].sort) {
      const f = cols[st.col].sort;
      rows = rows.slice().sort((a, b) => {
        const x = f(a.data), y = f(b.data);
        const r = typeof x === "string" ? x.localeCompare(y) : (x ?? -Infinity) - (y ?? -Infinity);
        return st.dir * r;
      });
    }
    const head = cols.map((c, i) => {
      const s = st && st.col === i ? (st.dir > 0 ? " ▲" : " ▼") : "";
      return `<th class="${c.right ? "r" : ""} ${c.sort ? "sortable" : ""}" data-tbl="${id}" data-col="${i}">${esc(c.label)}${s}</th>`;
    }).join("");
    const body = rows.map((r) => `<tr class="${r.cls || ""}" ${r.href ? `data-href="${esc(r.href)}"` : ""}>${r.cells.map((c, i) => `<td class="${cols[i].right ? "r" : ""}">${c}</td>`).join("")}</tr>`).join("");
    return `<div class="table-wrap"><table><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table></div>`;
  };
  document.addEventListener("click", (e) => {
    const th = e.target.closest("th.sortable");
    if (th) {
      const id = th.dataset.tbl, col = +th.dataset.col;
      const cur = sortState[id];
      sortState[id] = { col, dir: cur && cur.col === col ? -cur.dir : -1 };
      if (state.page && state.page.render) state.page.render(true);
      return;
    }
    const tr = e.target.closest("tr[data-href]");
    if (tr && !e.target.closest("a,button,input,label,select")) location.hash = tr.dataset.href;
  });

  // ---------- API ----------
  M.api = async function (path, opts = {}) {
    const init = { headers: { Accept: "application/json" }, cache: "no-store", method: opts.method || "GET" };
    if (init.method !== "GET") init.headers["X-Requested-With"] = "monitor";
    if (opts.body !== undefined) {
      if (opts.body instanceof FormData) init.body = opts.body;
      else { init.body = JSON.stringify(opts.body); init.headers["Content-Type"] = "application/json"; }
    }
    const r = await fetch(path, init);
    let data = null;
    try { data = await r.json(); } catch (_) { /* empty body */ }
    if (r.status === 401 && !opts.allow401) { M.showLogin(); throw new Error(t("auth.required")); }
    if (!r.ok) throw new Error((data && data.error) || r.status + "");
    return data;
  };
  M.history = async function (id, keys, from, to, points) {
    const q = new URLSearchParams({ from: Math.round(from), to: Math.round(to), points });
    for (const k of keys) q.append("key", k);
    const r = await M.api(`/api/v1/agents/${id}/history?${q}`);
    return r.series;
  };

  // ---------- live stream ----------
  let es = null;
  const listeners = {};
  M.on = (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); };
  const emit = (ev, d) => { for (const fn of listeners[ev] || []) fn(d); };

  function connect() {
    if (es) es.close();
    es = new EventSource("/api/v1/stream");
    es.addEventListener("snapshot", (e) => {
      const snap = JSON.parse(e.data);
      state.hub = snap.hub;
      state.clockOffset = snap.hub.now - Date.now();
      const reconnect = state.conn === "off";
      state.agents = new Map(snap.agents.map((a) => [a.id, a]));
      state.alerts = new Map((snap.alerts || []).map((a) => [a.id, a]));
      setConn("on");
      renderSidebar();
      if (reconnect || !state.page) route();
      else pageCall("onSnapshot");
    });
    es.addEventListener("sample", (e) => {
      const { id, s } = JSON.parse(e.data);
      const a = state.agents.get(id);
      if (!a) return;
      a.latest = s;
      a.last_seen = M.now();
      if (a.status !== "online") a.status = "online";
      const sp = state.spark.get(id) || [];
      sp.push(s.cpu ? s.cpu.usage : null);
      if (sp.length > M.SPARK_N) sp.splice(0, sp.length - M.SPARK_N);
      state.spark.set(id, sp);
      pageCall("onSample", id, s);
    });
    es.addEventListener("report", (e) => {
      const { id, kind, data } = JSON.parse(e.data);
      const a = state.agents.get(id);
      if (!a) return;
      a.reports = a.reports || {};
      a.reports[kind] = data;
      pageCall("onReport", id, kind);
    });
    es.addEventListener("status", (e) => {
      const m = JSON.parse(e.data);
      const a = state.agents.get(m.id);
      if (!a) return;
      a.status = m.status;
      a.last_seen = m.last_seen;
      renderSidebar();
      pageCall("onStatus", m.id);
    });
    es.addEventListener("health", (e) => {
      const m = JSON.parse(e.data);
      const a = state.agents.get(m.id);
      if (a) a.health = m.health;
      pageCall("onStatus", m.id);
    });
    es.addEventListener("agent", (e) => {
      const v = JSON.parse(e.data);
      const old = state.agents.get(v.id);
      state.agents.set(v.id, Object.assign({}, old, v, { latest: v.latest || (old && old.latest) }));
      renderSidebar();
      pageCall("onAgent", v.id);
    });
    es.addEventListener("removed", (e) => {
      const m = JSON.parse(e.data);
      state.agents.delete(m.id);
      renderSidebar();
      pageCall("onAgent", m.id);
    });
    es.addEventListener("backfill", (e) => pageCall("onBackfill", JSON.parse(e.data).id));
    es.addEventListener("alert", (e) => {
      const ev = JSON.parse(e.data);
      if (ev.kind === "resolved") state.alerts.delete(ev.alert.id);
      else state.alerts.set(ev.alert.id, ev.alert);
      renderSidebar();
      if (ev.kind === "opened") notifyAlert(ev.alert);
      emit("alert", ev);
      pageCall("onAlert", ev);
    });
    es.addEventListener("monitoring", (e) => {
      state.overrides = JSON.parse(e.data);
      pageCall("onReport");
    });
    es.onerror = () => {
      setConn("off");
      // The session may have expired: check once, show login if so.
      M.api("/api/v1/auth/state", { allow401: true }).then((s) => { if (!s.user) M.showLogin(); }).catch(() => {});
    };
  }
  M.reconnect = connect;

  function pageCall(name, ...args) {
    if (state.page && state.page[name]) {
      try { state.page[name](...args); } catch (err) { console.error(err); }
    }
  }

  function setConn(c) {
    state.conn = c;
    const el = $("#live");
    if (!el) return;
    el.classList.toggle("on", c === "on");
    el.classList.toggle("off", c === "off");
    $(".txt", el).textContent = t(c === "on" ? "live.on" : c === "off" ? "live.off" : "live.connecting");
  }

  // ---------- notifications ----------
  function prefs() {
    try { return JSON.parse(localStorage.getItem("prefs") || "{}"); } catch (_) { return {}; }
  }
  M.prefs = prefs;
  M.setPref = function (k, v) {
    const p = prefs();
    p[k] = v;
    try { localStorage.setItem("prefs", JSON.stringify(p)); } catch (_) { /* ignore */ }
  };
  let audioCtx = null;
  M.beep = function (critical) {
    try {
      audioCtx = audioCtx || new (window.AudioContext || window.webkitAudioContext)();
      const o = audioCtx.createOscillator(), g = audioCtx.createGain();
      o.type = "sine";
      o.frequency.value = critical ? 880 : 660;
      g.gain.setValueAtTime(0.0001, audioCtx.currentTime);
      g.gain.exponentialRampToValueAtTime(0.25, audioCtx.currentTime + 0.02);
      g.gain.exponentialRampToValueAtTime(0.0001, audioCtx.currentTime + (critical ? 0.6 : 0.3));
      o.connect(g).connect(audioCtx.destination);
      o.start();
      o.stop(audioCtx.currentTime + 0.65);
    } catch (_) { /* audio unavailable */ }
  };
  function notifyAlert(a) {
    M.toast(a.title, a.severity === "critical" ? "crit" : "warn", "#/alerts");
    const p = prefs();
    if (p.sound) M.beep(a.severity === "critical");
    if (p.browser && "Notification" in window && Notification.permission === "granted" && document.hidden) {
      try { new Notification("Monitor.uz", { body: a.title, icon: "/static/favicon.svg", tag: "alert-" + a.id }); } catch (_) { /* ignore */ }
    }
  }
  M.toast = function (text, kind = "ok", href = null) {
    const box = $("#toasts");
    if (!box) return;
    const el = document.createElement(href ? "a" : "div");
    el.className = "toast " + kind;
    if (href) el.href = href;
    el.innerHTML = `<i class="shape"></i><span></span>`;
    $("span", el).textContent = text;
    box.append(el);
    setTimeout(() => el.classList.add("hide"), 6000);
    setTimeout(() => el.remove(), 6600);
  };

  // ---------- shell ----------
  const NAV = [
    { id: "overview", href: "#/", icon: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>' },
    { id: "servers", href: "#/servers", icon: '<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/>' },
    { id: "vms", href: "#/vms", icon: '<path d="M12 3l8 4.5v9L12 21l-8-4.5v-9z"/><path d="M12 12l8-4.5M12 12v9M12 12L4 7.5"/>' },
    { id: "docker", href: "#/docker", icon: '<path d="M12 3l9 5-9 5-9-5z"/><path d="M3 13l9 5 9-5"/>' },
    { id: "disks", href: "#/disks", icon: '<ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6"/><path d="M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/>' },
    { id: "network", href: "#/network", icon: '<rect x="9" y="3" width="6" height="5" rx="1"/><rect x="3" y="16" width="6" height="5" rx="1"/><rect x="15" y="16" width="6" height="5" rx="1"/><path d="M12 8v4M6 16v-2h12v2"/>' },
    { id: "alerts", href: "#/alerts", icon: '<path d="M6 16V11a6 6 0 1 1 12 0v5l1.5 2h-15z"/><path d="M10 20a2 2 0 0 0 4 0"/>' },
    { id: "settings", href: "#/settings", icon: '<path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12"/><circle cx="16" cy="6" r="2"/><circle cx="10" cy="12" r="2"/><circle cx="18" cy="18" r="2"/>' },
  ];
  let activeNav = "overview";
  function renderNav() {
    const nCrit = [...state.alerts.values()].filter((a) => a.severity === "critical").length;
    const nAll = state.alerts.size;
    $("#nav").innerHTML = NAV.map((n) => {
      const icon = `<svg viewBox="0 0 24 24" aria-hidden="true">${n.icon}</svg>`;
      let badge = "";
      if (n.id === "alerts" && nAll) badge = `<small class="badge ${nCrit ? "crit" : "warn"}">${nAll}</small>`;
      return `<a href="${n.href}" class="${n.id === activeNav ? "active" : ""}">${icon}${esc(t("nav." + n.id))}${badge}</a>`;
    }).join("");
  }
  function renderSidebar() {
    renderNav();
    const hosts = M.hosts().length, guests = M.guests().length;
    $("#side-foot").innerHTML = `${esc(t("sidebar.hub", { v: state.hub ? state.hub.version : "–" }))}<br>${esc(t("sidebar.agents", { h: hosts, g: guests }))}` +
      (state.user ? `<br><span class="side-user">${esc(state.user.username)}</span> · <a href="#" id="logout-link">${esc(t("auth.logout"))}</a>` : "");
    const lo = $("#logout-link");
    if (lo) lo.addEventListener("click", async (e) => {
      e.preventDefault();
      await M.api("/api/v1/auth/logout", { method: "POST", allow401: true }).catch(() => {});
      location.reload();
    });
  }
  M.renderSidebar = renderSidebar;

  M.setTitle = function (title, sub) {
    $("#page-title").textContent = title;
    $("#page-sub").textContent = sub || "";
    document.title = title + " · Monitor.uz";
  };
  M.setTitleHTML = function (html, subHTML, plain) {
    $("#page-title").innerHTML = html;
    $("#page-sub").innerHTML = subHTML || "";
    document.title = (plain || "") + " · Monitor.uz";
  };

  // ---------- router ----------
  M.pages = {};
  const ROUTES = [
    [/^#\/?$/, "overview", "overview"],
    [/^#\/servers$/, "servers", "servers"],
    [/^#\/server\/(\d+)$/, "server", "servers"],
    [/^#\/vms$/, "vms", "vms"],
    [/^#\/vm\/(\d+)\/(\d+)$/, "vm", "vms"],
    [/^#\/guest\/(\d+)$/, "guest", "vms"],
    [/^#\/docker$/, "docker", "docker"],
    [/^#\/disks$/, "disks", "disks"],
    [/^#\/network$/, "network", "network"],
    [/^#\/alerts$/, "alerts", "alerts"],
    [/^#\/settings(?:\/(\w+))?$/, "settings", "settings"],
  ];
  function route() {
    if (!state.user) return;
    if (state.page && state.page.destroy) state.page.destroy();
    state.page = null;
    const view = $("#view");
    view.innerHTML = "";
    $("#page-actions").innerHTML = "";
    const h = location.hash || "#/";
    $(".app").classList.remove("menu-open");
    for (const [re, name, nav] of ROUTES) {
      const m = re.exec(h);
      if (m) {
        activeNav = nav;
        renderNav();
        state.page = M.pages[name](view, ...m.slice(1)) || {};
        window.scrollTo(0, 0);
        return;
      }
    }
    location.hash = "#/";
  }
  M.route = route;

  // Each page may re-render at most once per 700 ms however many events arrive.
  M.throttle = function (fn, ms = 700) {
    let timer = null, last = 0;
    return function (now) {
      if (now === true) { clearTimeout(timer); timer = null; last = Date.now(); fn(); return; }
      if (timer) return;
      timer = setTimeout(() => { timer = null; last = Date.now(); fn(); }, Math.max(0, ms - (Date.now() - last)));
    };
  };

  // ---------- theme ----------
  function applyTheme(theme) {
    document.documentElement.dataset.theme = theme;
    const btn = $("#theme-btn");
    btn.title = t(theme === "dark" ? "theme.light" : "theme.dark");
    btn.setAttribute("aria-label", t("theme.toggle"));
    btn.innerHTML = theme === "dark"
      ? '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>'
      : '<svg viewBox="0 0 24 24"><path d="M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z"/></svg>';
    pageCall("onTheme");
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
  M.startApp = async function (user) {
    state.user = user;
    $("#login").hidden = true;
    $(".app").hidden = false;
    try { state.overrides = await M.api("/api/v1/monitoring"); } catch (_) { /* default */ }
    renderSidebar();
    connect();
  };

  async function boot() {
    try {
      TEXT = await (await fetch("/i18n/uz.json", { cache: "no-store" })).json();
    } catch (e) {
      console.error("texts", e);
    }
    for (const el of $$("[data-i18n]")) el.textContent = t(el.dataset.i18n);
    for (const el of $$("[data-i18n-title]")) el.title = t(el.dataset.i18nTitle);
    for (const el of $$("[data-i18n-ph]")) el.placeholder = t(el.dataset.i18nPh);
    initTheme();
    setConn("connecting");
    $("#menu-btn").addEventListener("click", () => $(".app").classList.toggle("menu-open"));
    $(".scrim").addEventListener("click", () => $(".app").classList.remove("menu-open"));
    window.addEventListener("hashchange", route);
    let st;
    try {
      st = await M.api("/api/v1/auth/state", { allow401: true });
    } catch (e) {
      st = {};
    }
    if (st.user) M.startApp(st.user);
    else M.showLogin(st.setup_required);
  }
  document.addEventListener("DOMContentLoaded", boot);
  return M;
})();
