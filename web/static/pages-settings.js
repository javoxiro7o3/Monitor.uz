// Monitor.uz: settings (agents & versions, monitored VMs, notifications,
// users, own account, panel preferences).
"use strict";

(function () {
  const { $, $$, t, esc, fmt } = M;
  const TABS = ["agents", "monitoring", "notify", "users", "account", "panel"];

  function copyBtn(text) {
    return `<button type="button" class="btn small" data-copy="${esc(text)}">${esc(t("btn.copy"))}</button>`;
  }
  document.addEventListener("click", async (e) => {
    const b = e.target.closest("[data-copy]");
    if (!b) return;
    try {
      await navigator.clipboard.writeText(b.dataset.copy);
      M.toast(t("msg.copied"), "ok");
    } catch (_) {
      M.toast(t("msg.copy_failed"), "warn");
    }
  });

  async function confirmAsk(text) {
    return window.confirm(text);
  }

  M.pages.settings = function (view, tabArg) {
    const tab = TABS.includes(tabArg) ? tabArg : "agents";
    M.setTitle(t("settings.title"), t("settings.subtitle"));
    view.innerHTML = `<div class="tabs">${TABS.map((k) => `<a class="${k === tab ? "active" : ""}" href="#/settings/${k}">${esc(t("settings.tab_" + k))}</a>`).join("")}</div><div id="st-body"></div>`;
    const body = $("#st-body");
    const page = { agents, monitoring, notify, users, account, panel }[tab](body);
    return page || {};
  };

  // ---------- agents & versions ----------
  function agents(body) {
    let versions = { versions: [], default: "" };
    let tokenOut = null;
    async function loadVersions() {
      try { versions = await M.api("/api/v1/agent-versions"); } catch (_) { /* ignore */ }
      draw();
    }
    function draw() {
      const list = [...M.state.agents.values()].sort((a, b) => a.mode.localeCompare(b.mode) || a.name.localeCompare(b.name));
      const vopts = versions.versions.map((v) => v.version).filter((v, i, a) => a.indexOf(v) === i);
      body.innerHTML = `
        <div class="card"><div class="card-head"><h2>${esc(t("agents.add"))}</h2></div>
          <form class="form-row" id="tok-form">
            <label>${esc(t("agents.note"))}<input name="note" placeholder="srv-01" maxlength="60"></label>
            <label>${esc(t("agents.mode"))}<select name="mode"><option value="host">${esc(t("agents.mode_host"))}</option><option value="guest">${esc(t("agents.mode_guest"))}</option></select></label>
            <label>${esc(t("agents.ttl"))}<select name="ttl_hours"><option value="1">1 ${esc(t("unit.hour"))}</option><option value="24" selected>24 ${esc(t("unit.hour"))}</option><option value="168">7 ${esc(t("unit.day"))}</option></select></label>
            <button class="btn primary" type="submit">${esc(t("agents.create_token"))}</button>
          </form>
          ${tokenOut ? `<div class="token-out"><p>${esc(t(tokenOut.mode === "guest" ? "agents.cmd_guest" : "agents.cmd_host"))}</p>
            <div class="cmd-line"><code class="cmd">${esc(tokenOut.command)}</code>${copyBtn(tokenOut.command)}</div>
            <p class="dim small">${esc(t("agents.token_exp", { v: fmt.full(tokenOut.expires) }))}</p></div>` : `<p class="dim small">${esc(t("agents.add_note"))}</p>`}
        </div>
        <div class="section card"><div class="card-head"><h2>${esc(t("agents.list"))}</h2><span class="note">${esc(t("agents.count", { n: list.length }))}</span></div>
          ${M.table("agents", [
            { label: t("agents.c_name"), sort: (a) => a.name }, { label: t("agents.c_mode"), sort: (a) => a.mode }, { label: t("agents.c_status"), sort: (a) => a.status },
            { label: t("agents.c_version"), sort: (a) => a.info.version || "" }, { label: t("agents.c_seen"), right: 1, sort: (a) => a.last_seen }, { label: t("agents.c_actions") }],
          list.map((a) => ({ data: a, cls: a.revoked ? "row-dim" : "", cells: [
            `<b>${esc(a.name)}</b><div class="dim">${esc([a.info.os, a.info.arch].filter(Boolean).join(" · "))}</div>`,
            `<span class="tag">${esc(t("agents.mode_" + a.mode))}</span>`,
            M.statusHTML(a.revoked ? "off" : M.health(a), a.revoked ? t("health.revoked") : undefined),
            `${esc(a.info.version || "–")}${a.desired_version ? `<div class="txt-warn small">→ ${esc(a.desired_version)}</div>` : ""}<div class="dim small">${esc((a.info.tools || []).join(", "))}</div>`,
            a.last_seen ? esc(fmt.ago(a.last_seen)) : "–",
            a.revoked ? `<button class="btn small danger" data-del="${a.id}">${esc(t("btn.delete"))}</button>` :
              `<div class="btn-group"><select data-ver="${a.id}"><option value="">${esc(t("agents.update_to"))}</option>${vopts.filter((v) => v !== a.info.version).map((v) => `<option>${esc(v)}</option>`).join("")}</select>
               ${a.desired_version ? `<button class="btn small" data-cancel="${a.id}">${esc(t("btn.cancel"))}</button>` : ""}
               <button class="btn small danger" data-revoke="${a.id}">${esc(t("agents.revoke"))}</button></div>`,
          ] })), { empty: t("agents.none") })}
        </div>
        <div class="section card"><div class="card-head"><h2>${esc(t("versions.title"))}</h2><span class="note">${esc(t("versions.note"))}</span></div>
          ${M.table("versions", [{ label: t("versions.c_version") }, { label: t("versions.c_arch") }, { label: "SHA-256" }, { label: t("versions.c_size"), right: 1 }, { label: t("versions.c_date"), right: 1 }, { label: "" }],
            versions.versions.map((v) => ({ cells: [`<b>${esc(v.version)}</b>${v.version === versions.default ? ` <span class="tag accent">${esc(t("versions.default"))}</span>` : ""}`,
              esc(v.arch), `<span class="mono small">${esc(v.sha256.slice(0, 16))}…</span>`, esc(fmt.bytes(v.size)), esc(fmt.full(v.uploaded_at)),
              v.version === versions.default ? "" : `<button class="btn small" data-default="${esc(v.version)}">${esc(t("versions.make_default"))}</button>`] })), { empty: t("versions.none") })}
          <form class="form-row section" id="ver-form">
            <label>${esc(t("versions.file"))}<input type="file" name="file" required></label>
            <label>${esc(t("versions.c_version"))}<input name="version" placeholder="0.2.0" required pattern="[0-9A-Za-z][0-9A-Za-z._-]{0,31}"></label>
            <label>${esc(t("versions.c_arch"))}<select name="arch"><option>amd64</option><option>arm64</option></select></label>
            <button class="btn" type="submit">${esc(t("versions.upload"))}</button>
          </form>
          <p class="dim small">${esc(t("versions.howto"))}</p>
        </div>`;
      $("#tok-form", body).addEventListener("submit", async (e) => {
        e.preventDefault();
        const d = Object.fromEntries(new FormData(e.target).entries());
        d.ttl_hours = +d.ttl_hours;
        try {
          const r = await M.api("/api/v1/tokens", { method: "POST", body: d });
          tokenOut = Object.assign(r, { mode: d.mode });
          draw();
        } catch (err) { M.toast(err.message, "crit"); }
      });
      $("#ver-form", body).addEventListener("submit", async (e) => {
        e.preventDefault();
        const btn = $("button", e.target);
        btn.disabled = true;
        try {
          await M.api("/api/v1/agent-versions", { method: "POST", body: new FormData(e.target) });
          M.toast(t("versions.uploaded"), "ok");
          loadVersions();
        } catch (err) { M.toast(err.message, "crit"); btn.disabled = false; }
      });
    }
    body.addEventListener("click", async (e) => {
      const rv = e.target.closest("[data-revoke]"), dl = e.target.closest("[data-del]"), df = e.target.closest("[data-default]"), cn = e.target.closest("[data-cancel]");
      try {
        if (rv && await confirmAsk(t("agents.revoke_q"))) await M.api(`/api/v1/agents/${rv.dataset.revoke}/revoke`, { method: "POST" });
        if (dl && await confirmAsk(t("agents.delete_q"))) await M.api(`/api/v1/agents/${dl.dataset.del}`, { method: "DELETE" });
        if (df) { await M.api(`/api/v1/agent-versions/${encodeURIComponent(df.dataset.default)}/default`, { method: "POST" }); loadVersions(); }
        if (cn) await M.api(`/api/v1/agents/${cn.dataset.cancel}/update`, { method: "POST", body: { version: "" } });
      } catch (err) { M.toast(err.message, "crit"); }
    });
    body.addEventListener("change", async (e) => {
      const s = e.target.closest("select[data-ver]");
      if (!s || !s.value) return;
      const a = M.state.agents.get(+s.dataset.ver);
      if (!(await confirmAsk(t("agents.update_q", { n: a ? a.name : "", v: s.value })))) { s.value = ""; return; }
      try {
        await M.api(`/api/v1/agents/${s.dataset.ver}/update`, { method: "POST", body: { version: s.value } });
        M.toast(t("agents.update_sent"), "ok");
      } catch (err) { M.toast(err.message, "crit"); }
    });
    loadVersions();
    const render = M.throttle(draw, 2000);
    return { onAgent: () => render(), onStatus: () => render(), render: () => render(true) };
  }

  // ---------- monitored VMs ----------
  function monitoring(body) {
    function draw() {
      const all = M.allVMs().filter((x) => !x.vm.template);
      body.innerHTML = `<div class="card"><div class="card-head"><h2>${esc(t("mon.title"))}</h2></div><p class="muted">${esc(t("mon.text"))}</p>
        ${M.vmTable("mon-vms", all, true)}</div>`;
      M.applyStyles(body);
    }
    const render = M.throttle(draw, 3000);
    draw();
    return { onReport: () => render(), render: () => render(true) };
  }

  // ---------- notifications (webhook) ----------
  function notify(body) {
    async function draw() {
      let s;
      try { s = await M.api("/api/v1/alert-settings"); } catch (e) { body.innerHTML = M.empty(e.message); return; }
      const w = s.webhook, st = s.status;
      body.innerHTML = `<form class="card" id="wh-form"><div class="card-head"><h2>${esc(t("notify.title"))}</h2></div>
        <p class="muted">${esc(t("notify.text"))}</p>
        <label class="check"><input type="checkbox" name="enabled" ${w.enabled ? "checked" : ""}>${esc(t("notify.enabled"))}</label>
        <label>${esc(t("notify.url"))}<input name="url" type="url" autocomplete="off" placeholder="${esc(w.url_set ? t("notify.url_keep", { v: w.url_hint }) : "https://mattermost.lan/hooks/…")}"></label>
        ${w.url_set ? `<label class="check"><input type="checkbox" name="clear_url">${esc(t("notify.clear"))}</label>` : ""}
        <label>${esc(t("notify.channel"))}<input name="channel" value="${esc(w.channel || "")}" placeholder="monitoring"></label>
        <label class="check"><input type="checkbox" name="insecure" ${w.insecure ? "checked" : ""}>${esc(t("notify.insecure"))}</label>
        <div class="form-foot"><span class="err" id="wh-err"></span><button class="btn" type="button" id="wh-test">${esc(t("notify.test"))}</button><button class="btn primary" type="submit">${esc(t("btn.save"))}</button></div>
      </form>
      <div class="section card"><div class="card-head"><h2>${esc(t("notify.status"))}</h2></div><div class="kv">
        <span>${esc(t("notify.st_state"))}</span><b>${esc(st.enabled && st.configured ? t("notify.on") : t("notify.off"))}</b>
        <span>${esc(t("notify.st_sent"))}</span><b>${st.sent}</b>
        <span>${esc(t("notify.st_failed"))}</span><b class="${st.failed ? "txt-crit" : ""}">${st.failed}</b>
        <span>${esc(t("notify.st_queue"))}</span><b>${st.queued}</b>
        <span>${esc(t("notify.st_last_ok"))}</span><b>${st.last_ok ? esc(fmt.full(st.last_ok)) : "–"}</b>
        <span>${esc(t("notify.st_last_err"))}</span><b class="${st.last_error ? "txt-crit" : ""}">${st.last_error ? esc(st.last_error + " (" + fmt.full(st.last_error_at) + ")") : "–"}</b>
      </div></div>`;
      const f = $("#wh-form", body);
      const val = () => {
        const d = Object.fromEntries(new FormData(f).entries());
        return { enabled: !!d.enabled, url: d.url || "", clear_url: !!d.clear_url, insecure: !!d.insecure, channel: d.channel || "" };
      };
      f.addEventListener("submit", async (e) => {
        e.preventDefault();
        try {
          await M.api("/api/v1/alert-settings", { method: "PUT", body: { webhook: val() } });
          M.toast(t("msg.saved"), "ok");
          draw();
        } catch (err) { $("#wh-err").textContent = err.message; }
      });
      $("#wh-test", body).addEventListener("click", async () => {
        $("#wh-err").textContent = "";
        try {
          const r = await M.api("/api/v1/alert-settings/test", { method: "POST", body: val() });
          if (r.ok) M.toast(t("notify.test_ok"), "ok");
          else $("#wh-err").textContent = r.error;
        } catch (err) { $("#wh-err").textContent = err.message; }
      });
    }
    draw();
    return {};
  }

  // ---------- users ----------
  function users(body) {
    async function draw() {
      let r;
      try { r = await M.api("/api/v1/users"); } catch (e) { body.innerHTML = M.empty(e.message); return; }
      body.innerHTML = `<div class="card"><div class="card-head"><h2>${esc(t("users.title"))}</h2></div>
        ${M.table("users", [{ label: t("users.c_name") }, { label: "2FA" }, { label: t("users.c_last"), right: 1 }, { label: "" }],
          r.users.map((u) => ({ cells: [`<b>${esc(u.username)}</b>${u.id === r.me ? ` <span class="tag accent">${esc(t("users.you"))}</span>` : ""}`,
            u.totp_enabled ? M.statusHTML("ok", t("yes")) : `<span class="dim">${esc(t("no"))}</span>`, u.last_login ? esc(fmt.full(u.last_login)) : "–",
            u.id === r.me ? "" : `<button class="btn small danger" data-udel="${u.id}" data-name="${esc(u.username)}">${esc(t("btn.delete"))}</button>`] })))}
        <form class="form-row section" id="user-form">
          <label>${esc(t("auth.username"))}<input name="username" required autocomplete="off"></label>
          <label>${esc(t("auth.password"))}<input name="password" type="password" required autocomplete="new-password"></label>
          <button class="btn primary" type="submit">${esc(t("users.add"))}</button>
        </form><p class="dim small">${esc(t("users.note"))}</p></div>`;
      $("#user-form", body).addEventListener("submit", async (e) => {
        e.preventDefault();
        try {
          await M.api("/api/v1/users", { method: "POST", body: Object.fromEntries(new FormData(e.target).entries()) });
          M.toast(t("msg.saved"), "ok");
          draw();
        } catch (err) { M.toast(err.message, "crit"); }
      });
    }
    body.addEventListener("click", async (e) => {
      const b = e.target.closest("[data-udel]");
      if (!b || !(await confirmAsk(t("users.delete_q", { n: b.dataset.name })))) return;
      try { await M.api(`/api/v1/users/${b.dataset.udel}`, { method: "DELETE" }); draw(); } catch (err) { M.toast(err.message, "crit"); }
    });
    draw();
    return {};
  }

  // ---------- own account ----------
  function account(body) {
    let pending = null;
    async function draw() {
      const st = await M.api("/api/v1/auth/state");
      const u = st.user || {};
      body.innerHTML = `<div class="grid-2">
        <form class="card" id="pw-form"><div class="card-head"><h2>${esc(t("account.password"))}</h2></div>
          <label>${esc(t("account.old"))}<input name="old" type="password" required autocomplete="current-password"></label>
          <label>${esc(t("account.new"))}<input name="new" type="password" required autocomplete="new-password"></label>
          <label>${esc(t("auth.password2"))}<input name="new2" type="password" required autocomplete="new-password"></label>
          <div class="form-foot"><span class="err" id="pw-err"></span><button class="btn primary" type="submit">${esc(t("btn.save"))}</button></div></form>
        <div class="card"><div class="card-head"><h2>${esc(t("account.totp"))}</h2>${u.totp_enabled ? M.statusHTML("ok", t("account.totp_on")) : ""}</div>
          ${u.totp_enabled ? `<p class="muted">${esc(t("account.totp_on_text"))}</p>
            <form id="totp-off" class="form-row"><label>${esc(t("auth.password"))}<input name="password" type="password" required></label><button class="btn danger" type="submit">${esc(t("account.totp_disable"))}</button></form>`
          : pending ? `<p class="muted">${esc(t("account.totp_scan"))}</p>
            <div class="kv"><span>${esc(t("account.totp_key"))}</span><b class="mono">${esc(pending.secret.match(/.{1,4}/g).join(" "))}</b></div>
            <div class="cmd-line"><code class="cmd small">${esc(pending.uri)}</code>${copyBtn(pending.uri)}</div>
            <form id="totp-on" class="form-row section"><label>${esc(t("auth.code"))}<input name="code" inputmode="numeric" maxlength="6" required></label><button class="btn primary" type="submit">${esc(t("account.totp_confirm"))}</button></form>`
          : `<p class="muted">${esc(t("account.totp_text"))}</p><button class="btn primary" id="totp-begin">${esc(t("account.totp_enable"))}</button>`}
          <div class="err" id="totp-err"></div></div></div>`;
      $("#pw-form", body).addEventListener("submit", async (e) => {
        e.preventDefault();
        const d = Object.fromEntries(new FormData(e.target).entries());
        if (d.new !== d.new2) { $("#pw-err").textContent = t("auth.mismatch"); return; }
        try { await M.api("/api/v1/auth/password", { method: "POST", body: { old: d.old, new: d.new } }); M.toast(t("account.pw_changed"), "ok"); e.target.reset(); }
        catch (err) { $("#pw-err").textContent = err.message; }
      });
      const b = $("#totp-begin", body);
      if (b) b.addEventListener("click", async () => { pending = await M.api("/api/v1/auth/totp/begin", { method: "POST" }); draw(); });
      const on = $("#totp-on", body);
      if (on) on.addEventListener("submit", async (e) => {
        e.preventDefault();
        try { await M.api("/api/v1/auth/totp/enable", { method: "POST", body: Object.fromEntries(new FormData(e.target).entries()) }); pending = null; M.toast(t("account.totp_done"), "ok"); draw(); }
        catch (err) { $("#totp-err").textContent = err.message; }
      });
      const off = $("#totp-off", body);
      if (off) off.addEventListener("submit", async (e) => {
        e.preventDefault();
        try { await M.api("/api/v1/auth/totp/disable", { method: "POST", body: Object.fromEntries(new FormData(e.target).entries()) }); draw(); }
        catch (err) { $("#totp-err").textContent = err.message; }
      });
    }
    draw();
    return {};
  }

  // ---------- panel preferences ----------
  function panel(body) {
    function draw() {
      const p = M.prefs();
      const perm = "Notification" in window ? Notification.permission : "unsupported";
      body.innerHTML = `<div class="card"><div class="card-head"><h2>${esc(t("panel.title"))}</h2></div>
        <p class="muted">${esc(t("panel.text"))}</p>
        <label class="check"><input type="checkbox" id="pf-sound" ${p.sound ? "checked" : ""}>${esc(t("panel.sound"))}</label>
        <button class="btn small" id="pf-beep" type="button">${esc(t("panel.sound_test"))}</button>
        <label class="check section"><input type="checkbox" id="pf-browser" ${p.browser ? "checked" : ""} ${perm === "unsupported" ? "disabled" : ""}>${esc(t("panel.browser"))}</label>
        <p class="dim small">${esc(t("panel.perm_" + perm))}</p></div>
        <div class="section card"><div class="card-head"><h2>${esc(t("panel.about"))}</h2></div><div class="kv">
          <span>${esc(t("panel.hub_version"))}</span><b>${esc(M.state.hub ? M.state.hub.version : "–")}</b>
          <span>${esc(t("panel.history"))}</span><b>${esc(M.state.hub ? fmt.duration(M.state.hub.history_ms / 1000) : "–")}</b>
          <span>${esc(t("panel.started"))}</span><b>${esc(M.state.hub ? fmt.full(M.state.hub.started) : "–")}</b></div></div>`;
      $("#pf-sound", body).addEventListener("change", (e) => M.setPref("sound", e.target.checked));
      $("#pf-beep", body).addEventListener("click", () => M.beep(true));
      $("#pf-browser", body).addEventListener("change", async (e) => {
        if (e.target.checked && Notification.permission !== "granted") {
          const r = await Notification.requestPermission();
          if (r !== "granted") e.target.checked = false;
        }
        M.setPref("browser", e.target.checked);
        draw();
      });
    }
    draw();
    return {};
  }
})();
