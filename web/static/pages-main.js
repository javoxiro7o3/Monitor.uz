// Monitor.uz: login, overview, servers, server detail and shared widgets.
"use strict";

(function () {
  const { $, $$, t, esc, fmt } = M;

  // ---------- login / first admin ----------
  M.showLogin = async function (setupRequired) {
    if (setupRequired === undefined) {
      try { setupRequired = (await M.api("/api/v1/auth/state", { allow401: true })).setup_required; } catch (_) { setupRequired = false; }
    }
    M.state.user = null;
    $(".app").hidden = true;
    const box = $("#login");
    box.hidden = false;
    const form = $("#login-form");
    let needCode = false;
    function render(err) {
      form.innerHTML = setupRequired ? `
        <h2>${esc(t("setup.title"))}</h2>
        <p class="muted">${esc(t("setup.text"))}</p>
        <code class="cmd small">sudo cat /var/lib/monitor-hub/setup-code</code>
        <label>${esc(t("setup.code"))}<input name="code" autocomplete="one-time-code" required></label>
        <label>${esc(t("auth.username"))}<input name="username" autocomplete="username" required></label>
        <label>${esc(t("auth.password"))}<input name="password" type="password" autocomplete="new-password" required></label>
        <label>${esc(t("auth.password2"))}<input name="password2" type="password" autocomplete="new-password" required></label>
        <div class="err">${esc(err || "")}</div>
        <button class="btn primary" type="submit">${esc(t("setup.submit"))}</button>` : `
        <h2>${esc(t("auth.title"))}</h2>
        <label>${esc(t("auth.username"))}<input name="username" autocomplete="username" required ${needCode ? "readonly" : ""}></label>
        <label>${esc(t("auth.password"))}<input name="password" type="password" autocomplete="current-password" required ${needCode ? "readonly" : ""}></label>
        ${needCode ? `<label>${esc(t("auth.code"))}<input name="code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" required></label>` : ""}
        <div class="err">${esc(err || "")}</div>
        <button class="btn primary" type="submit">${esc(t("auth.submit"))}</button>`;
    }
    const keep = {};
    render();
    form.onsubmit = async (e) => {
      e.preventDefault();
      const d = Object.fromEntries(new FormData(form).entries());
      Object.assign(keep, d);
      const btn = $("button", form);
      btn.disabled = true;
      try {
        if (setupRequired) {
          if (d.password !== d.password2) throw new Error(t("auth.mismatch"));
          await M.api("/api/v1/auth/setup", { method: "POST", body: d, allow401: true });
        } else {
          const r = await M.api("/api/v1/auth/login", { method: "POST", body: { username: keep.username, password: keep.password, code: d.code || "" }, allow401: true });
          if (r.totp_required) {
            needCode = true;
            render();
            $("input[name=username]", form).value = keep.username;
            $("input[name=password]", form).value = keep.password;
            $("input[name=code]", form).focus();
            return;
          }
        }
        const st = await M.api("/api/v1/auth/state", { allow401: true });
        M.startApp(st.user);
      } catch (err) {
        render(err.message);
        if (!setupRequired && keep.username) $("input[name=username]", form).value = keep.username;
      } finally {
        const b = $("button", form);
        if (b) b.disabled = false;
      }
    };
    const first = $("input", form);
    if (first) first.focus();
  };

  // ---------- shared: range selector + chart panel ----------
  M.RANGES = {
    live: { span: 5 * 60e3, points: 300, label: "range.live" },
    "1h": { span: 3600e3, points: 720, label: "range.1h" },
    "24h": { span: 24 * 3600e3, points: 720, label: "range.24h" },
    "7d": { span: 7 * 24 * 3600e3, points: 1008, label: "range.7d" },
  };
  let savedRange = "live";
  try { savedRange = localStorage.getItem("range") || "live"; } catch (_) { /* ignore */ }
  if (!M.RANGES[savedRange]) savedRange = "live";

  /**
   * Chart panel bound to one agent's history.
   * defs: [{id, title, max, f, series: [[key, labelKey, colorVar]]}]
   * liveStep: expected spacing of live points (ms) for gap detection.
   */
  M.chartPanel = function (root, agentId, defs, opts = {}) {
    let range = savedRange;
    let data = {};
    let loading = false;
    let destroyed = false;
    const charts = {};
    const keys = defs.flatMap((c) => c.series.map((s) => s[0]));
    const liveStep = opts.liveStep || 1000;
    root.innerHTML = `<div class="toolbar"><h2>${esc(t("chart.title"))}</h2><div class="range-slot"></div></div>
      <div class="charts">${defs.map((c) => `
        <div class="card chart-card"><div class="card-head"><h2>${esc(t(c.title))}</h2></div>
          <div class="legend" data-lg="${c.id}"></div><div class="chart-wrap" data-ch="${c.id}"></div></div>`).join("")}</div>`;

    function seg() {
      const s = document.createElement("div");
      s.className = "seg";
      for (const k of Object.keys(M.RANGES)) {
        const b = document.createElement("button");
        b.type = "button";
        b.textContent = t(M.RANGES[k].label);
        b.className = k === range ? "active" : "";
        b.addEventListener("click", () => setRange(k));
        s.append(b);
      }
      $(".range-slot", root).replaceChildren(s);
    }
    function win() {
      const now = M.now();
      return { from: now - M.RANGES[range].span, to: now };
    }
    function legends() {
      for (const c of defs) {
        const el = $(`[data-lg="${c.id}"]`, root);
        el.innerHTML = c.series.map(([k, label, color]) => {
          const pts = data[k] || [];
          const last = pts.length ? pts[pts.length - 1][1] : null;
          return `<span><i data-bg="var(${color})"></i>${esc(t(label))}<b>${esc(c.f(last))}</b></span>`;
        }).join("");
        M.applyStyles(el);
      }
    }
    function draw(message) {
      const r = win();
      const gap = (span) => Math.max(liveStep * 5, (span / M.RANGES[range].points) * 3);
      for (const c of defs) {
        if (!charts[c.id]) {
          charts[c.id] = new MChart.LineChart($(`[data-ch="${c.id}"]`, root), {
            series: c.series.map(([key, label, color]) => ({ key, label: t(label), color })),
            max: c.max, format: c.f, emptyText: t("chart.no_data"), gapMs: gap,
            timeFormat: (ms, span) => fmt.clock(ms, span), tipTimeFormat: (ms) => fmt.full(ms),
          });
        }
        const sub = {};
        for (const [k] of c.series) sub[k] = data[k] || [];
        charts[c.id].setData(sub, r, message);
      }
      legends();
    }
    async function load() {
      if (loading) return;
      loading = true;
      draw(t("chart.loading"));
      const { from, to } = win();
      try {
        const h = await M.history(agentId, keys, from, to, M.RANGES[range].points);
        if (destroyed) return;
        data = h;
      } catch (e) {
        console.error(e);
      } finally {
        loading = false;
      }
      draw();
    }
    function setRange(k) {
      range = savedRange = k;
      try { localStorage.setItem("range", k); } catch (_) { /* ignore */ }
      seg();
      load();
    }
    seg();
    load();
    const periodic = setInterval(() => { if (range === "24h" || range === "7d") load(); else draw(); }, 30000);
    let reloadTimer = null;
    return {
      push(ts, vals) {
        if (range !== "live" && range !== "1h") return;
        const cut = M.now() - M.RANGES[range].span;
        for (const k of keys) {
          if (vals[k] == null) continue;
          const arr = data[k] || (data[k] = []);
          if (arr.length && arr[arr.length - 1][0] >= ts) continue;
          arr.push([ts, vals[k]]);
          let i = 0;
          while (i < arr.length && arr[i][0] < cut) i++;
          if (i) arr.splice(0, i);
        }
        draw();
      },
      reload() { clearTimeout(reloadTimer); reloadTimer = setTimeout(load, 1200); },
      redraw: () => draw(),
      destroy() {
        destroyed = true;
        clearInterval(periodic);
        clearTimeout(reloadTimer);
        for (const k in charts) charts[k].destroy();
      },
    };
  };

  M.HOST_CHARTS = [
    { id: "cpu", title: "chart.cpu", max: 100, f: (v, ax) => fmt.pct(v, ax), series: [["cpu.usage", "s.cpu_usage", "--s1"], ["cpu.iowait", "s.cpu_iowait", "--s2"]] },
    { id: "mem", title: "chart.mem", max: 100, f: (v, ax) => fmt.pct(v, ax), series: [["mem.used_pct", "s.mem_used", "--s1"], ["mem.swap_pct", "s.mem_swap", "--s3"]] },
    { id: "net", title: "chart.net", max: null, f: (v) => fmt.bits(v), series: [["net.rx_bps", "s.net_rx", "--s1"], ["net.tx_bps", "s.net_tx", "--s2"]] },
    { id: "disk", title: "chart.disk", max: null, f: (v) => fmt.bytesRate(v), series: [["disk.read_bps", "s.disk_read", "--s1"], ["disk.write_bps", "s.disk_write", "--s2"]] },
    { id: "psi", title: "chart.psi", max: null, f: (v, ax) => fmt.pct(v, ax), series: [["psi.cpu_some", "s.psi_cpu", "--s1"], ["psi.mem_some", "s.psi_mem", "--s3"], ["psi.io_some", "s.psi_io", "--s2"]] },
    { id: "load", title: "chart.load", max: null, f: (v) => fmt.num(v), series: [["load.1", "s.load1", "--s1"], ["load.5", "s.load5", "--s2"], ["load.15", "s.load15", "--s4"]] },
  ];

  // ---------- shared: server card ----------
  M.serverCard = function (a) {
    const s = a.latest || {};
    const on = M.online(a);
    const c = on && s.cpu ? s.cpu.usage : null;
    const m = on && s.mem ? s.mem.used_pct : null;
    const f = on ? M.maxFS(s) : null;
    const r = M.reports(a);
    const vms = (r.vms && r.vms.vms) || [];
    const raid = M.raidSummary(r.storage);
    const sub = [a.info && a.info.os, a.hostname !== a.name ? a.hostname : ""].filter(Boolean).join(" · ");
    const foot = on
      ? `<span>${esc(t("card.vms", { n: vms.filter((v) => !v.template).length }))} · ${esc(t("card.uptime", { v: fmt.duration(s.uptime || 0) }))}</span><span class="${raid.cls}">${esc(raid.text)}</span>`
      : `<span>${esc(t("card.seen", { v: a.last_seen ? fmt.ago(a.last_seen) : "–" }))}</span><span></span>`;
    return `<a class="card srv ${on ? "" : "is-off"}" href="#/server/${a.id}">
      <div class="srv-head"><div><div class="srv-name">${esc(a.name)}</div><div class="srv-sub">${esc(sub)}</div></div>${M.statusHTML(M.health(a))}</div>
      <div class="bars">
        <div class="bar-row"><span>${esc(t("card.cpu"))}</span>${M.bar(c, "cpu")}<span class="v">${fmt.pct(c)}</span></div>
        <div class="bar-row"><span>${esc(t("card.ram"))}</span>${M.bar(m, "ram")}<span class="v">${fmt.pct(m)}</span></div>
        <div class="bar-row"><span>${esc(t("card.disk"))}</span>${M.bar(f && f.used_pct, "disk")}<span class="v">${fmt.pct(f && f.used_pct)}</span></div>
      </div>
      <canvas class="spark" data-id="${a.id}"></canvas>
      <div class="srv-foot">${foot}</div></a>`;
  };
  M.drawSparks = function (root) {
    for (const cv of $$("canvas.spark[data-id]", root)) MChart.sparkline(cv, M.state.spark.get(+cv.dataset.id) || [], "--accent", 100);
  };
  M.primeSparks = async function (agents, after) {
    const now = M.now();
    await Promise.all(agents.map(async (a) => {
      if (M.state.spark.has(a.id)) return;
      M.state.spark.set(a.id, []);
      try {
        const h = await M.history(a.id, ["cpu.usage"], now - M.SPARK_N * 1000, now, M.SPARK_N);
        M.state.spark.set(a.id, h["cpu.usage"].map((p) => p[1]));
      } catch (_) { /* stays empty */ }
    }));
    if (after) after();
  };

  M.raidSummary = function (st) {
    if (!st) return { text: t("raid.none"), cls: "dim" };
    const lds = st.logical || [];
    const bad = lds.filter((l) => ["degraded", "rebuilding", "failed"].includes(l.state));
    const pbad = (st.physical || []).filter((p) => p.state === "failed" || p.state === "warning");
    if (bad.length) return { text: t("raid.state." + bad[0].state), cls: "txt-crit" };
    if (pbad.length) return { text: t("raid.disk_bad", { n: pbad.length }), cls: "txt-crit" };
    if (!lds.length && !(st.physical || []).length) return { text: st.source === "none" ? t("raid.none") : t("raid.ok"), cls: "dim" };
    return { text: t("raid.ok"), cls: "" };
  };

  // ---------- overview ----------
  M.pages.overview = function (view) {
    const render = M.throttle(draw, 1000);
    function draw() {
      const hosts = M.hosts();
      M.setTitle(t("overview.title"), t("overview.subtitle"));
      if (!hosts.length && !M.guests().length) {
        view.innerHTML = `<div class="card empty"><h2>${esc(t("overview.empty_title"))}</h2>
          <p>${esc(t("overview.empty_text"))}</p><a class="btn primary" href="#/settings/agents">${esc(t("overview.empty_btn"))}</a></div>`;
        return;
      }
      const online = hosts.filter(M.online);
      const vms = M.allVMs().filter((x) => !x.vm.template && M.vmMonitored(x.host.id, x.vm));
      const running = vms.filter((x) => x.vm.status === "running").length;
      const alerts = [...M.state.alerts.values()];
      const crit = alerts.filter((a) => a.severity === "critical").length;
      let disksTotal = 0, disksBad = 0, badHost = null;
      for (const h of hosts) {
        const ph = (M.reports(h).storage && M.reports(h).storage.physical) || [];
        disksTotal += ph.length;
        const b = ph.filter((p) => p.state === "failed" || p.state === "warning").length;
        disksBad += b;
        if (b && !badHost) badHost = h;
      }
      const offline = hosts.length - online.length;
      const kpis = `<div class="kpis">
        <div class="card kpi"><div class="label">${esc(t("overview.servers"))}</div>
          <div class="value">${online.length} <small>/ ${hosts.length}</small></div>
          <div class="note">${offline ? M.statusHTML("off", t("overview.servers_note_some", { n: offline })) : `<span class="dot ok"></span>${esc(t("overview.servers_note_all"))}`}</div></div>
        <div class="card kpi"><div class="label">${esc(t("overview.vms"))}</div>
          <div class="value">${vms.length}</div>
          <div class="note">${esc(t("overview.vms_note", { r: running, s: vms.length - running }))}</div></div>
        <div class="card kpi"><a href="#/alerts" class="label">${esc(t("overview.alerts"))}</a>
          <div class="value ${crit ? "txt-crit" : alerts.length ? "txt-warn" : ""}">${alerts.length}</div>
          <div class="note">${alerts.length ? esc(t("overview.alerts_note", { c: crit, w: alerts.length - crit })) : esc(t("overview.alerts_none"))}</div></div>
        <div class="card kpi"><a href="#/disks" class="label">${esc(t("overview.disks"))}</a>
          <div class="value">${disksTotal - disksBad} <small>/ ${disksTotal}</small></div>
          <div class="note ${disksBad ? "txt-crit" : ""}">${disksBad ? esc(t("overview.disks_bad", { n: disksBad, h: badHost.name })) : esc(t("overview.disks_ok"))}</div></div>
      </div>`;
      const cards = hosts.map(M.serverCard).join("");

      // Alerts (newest first)
      const al = alerts.sort((a, b) => (b.severity === "critical") - (a.severity === "critical") || b.started_at - a.started_at).slice(0, 6);
      const alertsHTML = al.length ? al.map((a) => `<a class="alert-item" href="#/alerts">${M.sevShape(a.severity)}<div><b>${esc(a.title)}</b>
        <div class="dim">${esc(M.state.agents.get(a.agent_id) ? M.state.agents.get(a.agent_id).name : "")} · ${esc(fmt.ago(a.started_at))}</div></div></a>`).join("")
        : `<div class="dim empty-row">${esc(t("overview.alerts_none"))}</div>`;

      // Disks of the worst server
      let diskHost = badHost || hosts.find((h) => M.reports(h).storage && (M.reports(h).storage.physical || []).length);
      const disksHTML = diskHost ? M.baysHTML(M.reports(diskHost).storage, true) : M.empty(t("disks.none"));

      // Docker top consumers
      const top = M.containers().filter((x) => x.c.state === "running").sort((a, b) => b.c.cpu - a.c.cpu).slice(0, 5);
      const topHTML = top.length ? top.map((x) => `<div class="top-row"><div><b>${esc(x.c.name)}</b>
        <div class="dim">${esc(M.guestLabel(x.guest))}</div></div>${M.bar(Math.min(100, x.c.cpu), "cpu")}<span class="num">${fmt.pct(x.c.cpu)}</span></div>`).join("")
        : M.empty(t("docker.none"));

      view.innerHTML = kpis +
        `<div class="section"><div class="section-title"><h2>${esc(t("nav.servers"))}</h2><a class="link" href="#/servers">${esc(t("overview.all"))}</a></div><div class="servers">${cards}</div></div>
        <div class="section grid-2">
          <div class="card"><div class="card-head"><h2>${esc(t("overview.active_alerts"))}</h2><a class="link" href="#/alerts">${esc(t("overview.all"))}</a></div>${alertsHTML}</div>
          <div class="card"><div class="card-head"><h2>${esc(t("overview.disk_raid"))}</h2><span class="note">${diskHost ? esc(t("overview.disk_note", { h: diskHost.name, n: ((M.reports(diskHost).storage || {}).physical || []).length })) : ""}</span></div>${disksHTML}</div>
        </div>
        <div class="section card"><div class="card-head"><h2>${esc(t("overview.docker_top"))}</h2><span class="note">${esc(t("overview.docker_note"))}</span></div>${topHTML}</div>`;
      M.applyStyles(view);
      M.drawSparks(view);
    }
    draw();
    M.primeSparks(M.hosts(), () => render());
    const tick = setInterval(() => render(), 5000);
    return { onSample: () => render(), onStatus: () => render(), onAgent: () => render(), onReport: () => render(), onAlert: () => render(),
      onSnapshot: () => render(true), onTheme: () => render(true), render: () => render(true), destroy: () => clearInterval(tick) };
  };

  M.sevShape = (sev) => `<span class="shape-only ${sev === "critical" ? "crit" : "warn"}"></span>`;
  M.guestLabel = function (g) {
    if (g.link) {
      const h = M.state.agents.get(g.link.host_id);
      return (h ? h.name : "?") + " · " + g.link.vm_name;
    }
    return g.name;
  };

  // ---------- servers list ----------
  M.pages.servers = function (view) {
    const render = M.throttle(draw, 1000);
    function draw() {
      M.setTitle(t("servers.title"), t("servers.subtitle"));
      const hosts = M.hosts();
      view.innerHTML = hosts.length ? `<div class="servers">${hosts.map(M.serverCard).join("")}</div>`
        : `<div class="card empty"><h2>${esc(t("overview.empty_title"))}</h2><a class="btn primary" href="#/settings/agents">${esc(t("overview.empty_btn"))}</a></div>`;
      M.applyStyles(view);
      M.drawSparks(view);
    }
    draw();
    M.primeSparks(M.hosts(), () => render());
    const tick = setInterval(() => render(), 5000);
    return { onSample: () => render(), onStatus: () => render(), onAgent: () => render(), onReport: () => render(),
      onSnapshot: () => render(true), onTheme: () => render(true), destroy: () => clearInterval(tick) };
  };

  // ---------- disk bays (shared with disks page) ----------
  M.diskState = function (p) {
    if (p.state === "failed") return ["crit", t("disk.st.failed")];
    if (p.state === "warning") return ["crit", t("disk.st.warning")];
    if (p.state === "rebuilding") return ["warn", t("disk.st.rebuilding")];
    if (p.role === "spare") return ["off", t("disk.st.spare")];
    if (p.role === "unassigned") return ["off", t("disk.st.unassigned")];
    if (p.state === "ok") return ["ok", t("disk.st.ok")];
    return ["off", t("disk.st.unknown")];
  };
  M.baysHTML = function (st, compact) {
    if (!st) return M.empty(t("disks.none"));
    const ph = st.physical || [];
    let html = "";
    if (st.errors && st.errors.length && !ph.length) html += `<div class="banner neutral small">${esc(st.errors.join("; "))}</div>`;
    if (ph.length) {
      html += `<div class="bays">${ph.map((p) => {
        const [cls, label] = M.diskState(p);
        const title = [p.id, p.model, p.serial && "SN " + p.serial, p.temp && fmt.temp(p.temp), p.power_on_hours && t("disk.hours", { n: fmt.int(p.power_on_hours) })].filter(Boolean).join(" · ");
        return `<div class="bay ${cls}" title="${esc(title)}"><div class="bay-n">${esc(p.bay || p.id.split(":").pop().replace("/dev/", ""))}</div>
          <div class="bay-s">${esc(label)}</div><div class="bay-z">${esc(fmt.size(p.size))}</div></div>`;
      }).join("")}</div>`;
    }
    const lds = st.logical || [];
    if (lds.length) {
      html += `<div class="ld-list">${lds.map((l) => {
        const cls = { ok: "ok", degraded: "crit", failed: "crit", rebuilding: "warn" }[l.state] || "off";
        const members = (l.members || []).map((m) => {
          const p = ph.find((x) => x.id === m);
          return p && p.bay ? p.bay : m;
        });
        const desc = l.controller === "ZFS" ? "ZFS · " + l.id : l.controller === "mdadm" ? "mdadm · " + l.id + " (RAID " + l.raid + ")"
          : t("disks.ld_desc", { raid: l.raid || "?", disks: members.join(", ") || "–" });
        const prog = l.state === "rebuilding" && l.progress ? " " + Math.round(l.progress) + "%" : "";
        return `<div class="ld-row"><span>${esc(desc)}${l.spares && l.spares.length ? esc(" · " + t("disks.spares", { n: l.spares.length })) : ""}</span><span class="txt-${cls}">${esc(t("raid.state." + l.state) + prog)}</span></div>`;
      }).join("")}</div>`;
    }
    if (!compact && st.controllers) {
      html += st.controllers.map((c) => `<div class="ld-row dim"><span>${esc(c.name)}${c.slot ? " · slot " + esc(c.slot) : ""}</span>
        <span>${esc(t("disks.ctrl_status", { s: c.status, c: c.cache || "–", b: c.battery || "–" }))}</span></div>`).join("");
    }
    return html || M.empty(t("disks.none"));
  };

  // ---------- server detail ----------
  M.pages.server = function (view, idStr) {
    const id = +idStr;
    const a0 = M.state.agents.get(id);
    if (!a0) {
      M.setTitle(t("server.not_found"));
      view.innerHTML = `<div class="card empty"><h2>${esc(t("server.not_found"))}</h2><a class="link" href="#/servers">← ${esc(t("nav.servers"))}</a></div>`;
      return {};
    }
    view.innerHTML = `
      <div id="srv-banner"></div>
      <div class="kpis k7" id="kpis"></div>
      <div id="charts"></div>
      <div class="section card"><div class="card-head"><h2>${esc(t("server.vms"))}</h2><a class="link" href="#/vms">${esc(t("overview.all"))}</a></div><div id="vm-table"></div></div>
      <div class="section grid-2">
        <div class="card"><div class="card-head"><h2>${esc(t("server.disks"))}</h2><span class="note" id="disk-src"></span></div><div id="storage"></div></div>
        <div class="card"><div class="card-head"><h2>${esc(t("server.sensors"))}</h2><span class="note" id="sens-src"></span></div><div id="sensors"></div></div>
      </div>
      <div class="section card"><div class="card-head"><h2>${esc(t("cores.title"))}</h2><span class="note" id="cores-note"></span></div><div class="cores" id="cores"></div></div>
      <div class="section card"><div class="card-head"><h2>${esc(t("net.title"))}</h2></div><div id="net-table"></div></div>
      <div class="section grid-2">
        <div class="card"><div class="card-head"><h2>${esc(t("disk.title"))}</h2></div><div id="disk-table"></div></div>
        <div class="card"><div class="card-head"><h2>${esc(t("fs.title"))}</h2></div><div id="fs-table"></div></div>
      </div>`;
    const panel = M.chartPanel($("#charts", view), id, M.HOST_CHARTS, { liveStep: 1000 });
    const agent = () => M.state.agents.get(id) || a0;

    function header() {
      const a = agent();
      const inf = a.info || {};
      const s = a.latest || {};
      const meta = [
        inf.os, inf.kernel, inf.cpu_model,
        inf.cpu_cores ? t("server.meta_cores", { n: inf.cpu_cores }) : "",
        inf.mem_total ? fmt.bytes(inf.mem_total) + " RAM" : "",
        M.online(a) && s.uptime ? t("server.uptime", { v: fmt.duration(s.uptime) }) : "",
        inf.version ? t("server.agent", { v: inf.version }) : "",
      ].filter(Boolean);
      M.setTitleHTML(`<a class="crumb" href="#/servers">← ${esc(t("nav.servers"))}</a>
        <span class="title-row"><span>${esc(a.name)}</span>${M.statusHTML(M.health(a))}</span>`,
        `<div class="meta">${meta.map((m) => `<span>${esc(m)}</span>`).join("")}</div>`, a.name);
      let banner = "";
      if (a.status === "offline") banner = `<div class="banner">${M.statusHTML("off")}<div>${esc(t("server.offline_banner", { v: a.last_seen ? fmt.ago(a.last_seen) : "–" }))}</div></div>`;
      else if (a.status === "new") banner = `<div class="banner neutral">${esc(t("server.new_banner"))}</div>`;
      if (s.na && s.na.length && M.online(a)) banner += `<div class="banner neutral">${esc(t("server.na", { v: s.na.join(", ") }))}</div>`;
      const al = [...M.state.alerts.values()].filter((x) => x.agent_id === id);
      if (al.length) banner += `<div class="card alerts-inline">${al.map((x) => `<a class="alert-item" href="#/alerts">${M.sevShape(x.severity)}<div><b>${esc(x.title)}</b><div class="dim">${esc(x.detail)} · ${esc(fmt.ago(x.started_at))}</div></div></a>`).join("")}</div>`;
      $("#srv-banner").innerHTML = banner;
    }

    function kpis() {
      const a = agent();
      const s = M.online(a) ? a.latest || {} : {};
      const m = s.mem;
      const p = M.pick(s);
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

    function cores() {
      const a = agent();
      const cs = (M.online(a) && a.latest && a.latest.cpu && a.latest.cpu.cores) || [];
      $("#cores-note").textContent = cs.length ? t("cores.note", { n: cs.length }) : "";
      const box = $("#cores");
      if (box.children.length !== cs.length || !cs.length) {
        box.innerHTML = cs.map(() => `<div class="core"></div>`).join("") || M.empty();
      }
      cs.forEach((v, i) => {
        const el = box.children[i];
        el.textContent = Math.round(v);
        el.title = t("cores.cell", { i, v: fmt.pct(v) });
        el.style.background = `color-mix(in srgb, var(--${v >= 90 ? "warn" : "accent"}) ${Math.round(8 + v * 0.75)}%, var(--track))`;
      });
    }

    function tables() {
      const a = agent();
      const s = a.latest || {};
      const live = M.online(a);
      const order = { phys: 0, bond: 1, bridge: 2, vlan: 3, other: 4 };
      const nets = (s.net || []).slice().sort((x, y) => (order[x.kind] - order[y.kind]) || x.name.localeCompare(y.name));
      const ifs = new Map(((M.reports(a).net || {}).ifaces || []).map((i) => [i.name, i]));
      $("#net-table").innerHTML = M.table("srv-net", [
        { label: t("net.name") }, { label: t("net.kind") }, { label: t("net.addr") }, { label: t("net.rx"), right: 1 }, { label: t("net.tx"), right: 1 },
        { label: t("net.pps"), right: 1 }, { label: t("net.errs"), right: 1 }, { label: t("net.drops"), right: 1 }],
      live ? nets.map((n) => {
        const inf = ifs.get(n.name) || {};
        const extra = [inf.speed ? inf.speed >= 1000 ? inf.speed / 1000 + " Gbit/s" : inf.speed + " Mbit/s" : "", inf.master ? "→ " + inf.master : ""].filter(Boolean).join(" ");
        return { cells: [`<b>${esc(n.name)}</b>${extra ? `<div class="dim">${esc(extra)}</div>` : ""}`, `<span class="tag">${esc(t("kind." + n.kind))}</span>`,
          `<span class="mono">${esc((inf.addrs || []).join(", ") || "–")}</span>`,
          fmt.bits(n.rx_bps), fmt.bits(n.tx_bps), fmt.count(n.rx_pps) + " / " + fmt.count(n.tx_pps),
          n.errs > 0 ? M.statusHTML("warn", fmt.count(n.errs)) : "0", n.drops > 0 ? fmt.count(n.drops) : "0"] };
      }) : []);
      $("#disk-table").innerHTML = M.table("srv-disk", [
        { label: t("disk.name") }, { label: t("disk.read"), right: 1 }, { label: t("disk.write"), right: 1 }, { label: t("disk.iops"), right: 1 },
        { label: t("disk.util"), right: 1 }, { label: t("disk.await"), right: 1 }],
      live ? (s.disks || []).map((d) => ({ cells: [`<b>${esc(d.name)}</b>`, fmt.bytesRate(d.read_bps), fmt.bytesRate(d.write_bps),
        fmt.count(d.read_iops) + " / " + fmt.count(d.write_iops), fmt.pct(d.util), fmt.ms(d.await_ms)] })) : []);
      $("#fs-table").innerHTML = M.table("srv-fs", [{ label: t("fs.mount") }, { label: t("fs.type") }, { label: t("fs.size"), right: 1 }, { label: t("fs.used"), right: 1 }],
        (s.fs || []).map((f) => ({ cells: [`<b>${esc(f.mount)}</b><div class="dim mono">${esc(f.device)}</div>`, `<span class="tag">${esc(f.type)}</span>`,
          fmt.bytes(f.total), `${M.bar(f.used_pct, "disk")}<span class="num">${fmt.pct(f.used_pct)}</span>`] })));
      M.applyStyles($("#fs-table"));
    }

    function reports() {
      const a = agent();
      const r = M.reports(a);
      // VMs
      const vms = ((r.vms && r.vms.vms) || []).filter((v) => !v.template);
      $("#vm-table").innerHTML = M.vmTable("srv-vms", vms.map((vm) => ({ host: a, vm })), false) +
        (r.vms && r.vms.error ? `<div class="banner neutral small">${esc(r.vms.error)}</div>` : "") +
        (!r.vms && M.online(a) ? M.empty(t("server.no_pve")) : "");
      // Storage
      $("#disk-src").textContent = r.storage ? t("src." + r.storage.source) : "";
      $("#storage").innerHTML = M.baysHTML(r.storage, false);
      // Sensors
      const sn = r.sensors;
      $("#sens-src").textContent = sn ? t("src." + sn.source) : "";
      if (!sn || (!(sn.temps || []).length && !(sn.fans || []).length && !(sn.psus || []).length)) {
        $("#sensors").innerHTML = M.empty(sn && sn.error ? sn.error : t("sensors.none"));
      } else {
        const st = (x) => ({ ok: "ok", warning: "warn", critical: "crit" }[x.status] || "off");
        const row = (x, val) => `<div class="sens-row"><span>${esc(x.name)}</span><span class="num">${esc(val)}</span>${M.statusHTML(st(x), t("sens." + x.status))}</div>`;
        $("#sensors").innerHTML =
          ((sn.temps || []).length ? `<h3 class="sub-h">${esc(t("sensors.temps"))}</h3>` + sn.temps.map((x) => row(x, fmt.temp(x.value))).join("") : "") +
          ((sn.fans || []).length ? `<h3 class="sub-h">${esc(t("sensors.fans"))}</h3>` + sn.fans.map((x) => row(x, x.unit === "%" ? fmt.pct(x.value) : fmt.int(x.value) + " RPM")).join("") : "") +
          ((sn.psus || []).length ? `<h3 class="sub-h">${esc(t("sensors.psus"))}</h3>` + sn.psus.map((x) => row(x, x.unit === "W" ? Math.round(x.value) + " W" : "")).join("") : "");
      }
      M.applyStyles(view);
    }

    const renderAll = M.throttle(() => { header(); kpis(); cores(); tables(); reports(); }, 700);
    renderAll(true);
    const tick = setInterval(() => header(), 15000);
    return {
      onSample(aid, s) {
        if (aid !== id) return;
        panel.push(s.ts, M.pick(s));
        renderAll();
      },
      onReport(aid) { if (aid === undefined || aid === id) renderAll(); },
      onStatus(aid) { if (aid === id) renderAll(); },
      onAgent(aid) { if (aid === id) renderAll(); },
      onAlert() { renderAll(); },
      onSnapshot() { renderAll(true); panel.reload(); },
      onBackfill(aid) { if (aid === id) panel.reload(); },
      onTheme() { panel.redraw(); renderAll(true); },
      render: () => renderAll(true),
      destroy() { clearInterval(tick); panel.destroy(); },
    };
  };
})();
