// Monitor.uz: small canvas charts (line chart + sparkline). No external
// libraries: the panel must work without internet.
"use strict";

(function (global) {
  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  function setupCanvas(canvas) {
    const dpr = window.devicePixelRatio || 1;
    const w = canvas.clientWidth, h = canvas.clientHeight;
    if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(h * dpr)) {
      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(h * dpr);
    }
    const ctx = canvas.getContext("2d");
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, h);
    return { ctx, w, h };
  }

  // Draws one series as a path, breaking it where consecutive points are
  // further apart than gapMs (missing data is not interpolated).
  function tracePath(ctx, pts, x, y, gapMs) {
    let prevT = null;
    ctx.beginPath();
    for (const [t, v] of pts) {
      if (v == null || Number.isNaN(v)) { prevT = null; continue; }
      if (prevT == null || t - prevT > gapMs) ctx.moveTo(x(t), y(v));
      else ctx.lineTo(x(t), y(v));
      prevT = t;
    }
  }

  function niceMax(v) {
    if (v <= 0) return 1;
    const p = Math.pow(10, Math.floor(Math.log10(v)));
    for (const m of [1, 2, 2.5, 5, 10]) if (v <= m * p) return m * p;
    return 10 * p;
  }

  // Binary search: index of the point nearest to t.
  function nearest(pts, t) {
    let lo = 0, hi = pts.length - 1;
    if (hi < 0) return -1;
    while (hi - lo > 1) {
      const mid = (lo + hi) >> 1;
      if (pts[mid][0] < t) lo = mid; else hi = mid;
    }
    return Math.abs(pts[lo][0] - t) <= Math.abs(pts[hi][0] - t) ? lo : hi;
  }

  /**
   * LineChart
   * opts: { series: [{key, label, color}], max: number|null, format: v => string,
   *         timeFormat: t => string, emptyText, gapMs }
   */
  class LineChart {
    constructor(wrap, opts) {
      this.wrap = wrap;
      this.opts = opts;
      this.canvas = document.createElement("canvas");
      this.msg = document.createElement("div");
      this.msg.className = "chart-msg";
      this.tip = document.createElement("div");
      this.tip.className = "tip";
      this.tip.hidden = true;
      wrap.append(this.canvas, this.msg, this.tip);
      this.data = {};
      this.range = null;
      this.hoverT = null;
      this.canvas.addEventListener("mousemove", (e) => this.onMove(e));
      this.canvas.addEventListener("mouseleave", () => { this.hoverT = null; this.tip.hidden = true; this.draw(); });
      this.canvas.addEventListener("touchstart", (e) => this.onMove(e.touches[0]), { passive: true });
      this.canvas.addEventListener("touchmove", (e) => this.onMove(e.touches[0]), { passive: true });
      this.ro = new ResizeObserver(() => this.draw());
      this.ro.observe(wrap);
    }

    destroy() { this.ro.disconnect(); }

    setData(data, range, message) {
      this.data = data;
      this.range = range;
      this.message = message || null;
      this.draw();
    }

    layout() {
      return { left: this.left || 44, right: 10, top: 8, bottom: 22 };
    }

    scales(w, h) {
      const L = this.layout();
      const { from, to } = this.range;
      let max = this.opts.max;
      if (max == null) {
        let m = 0;
        for (const s of this.opts.series) for (const p of this.data[s.key] || []) if (p[1] > m) m = p[1];
        max = niceMax(m * 1.1);
      }
      const x = (t) => L.left + ((t - from) / (to - from)) * (w - L.left - L.right);
      const y = (v) => h - L.bottom - (Math.min(v, max) / max) * (h - L.top - L.bottom);
      const t = (px) => from + ((px - L.left) / (w - L.left - L.right)) * (to - from);
      return { x, y, t, max, L };
    }

    draw() {
      if (!this.range) return;
      const { ctx, w, h } = setupCanvas(this.canvas);
      if (w === 0 || h === 0) return;
      const fmt = this.opts.format;
      ctx.font = "11px Inter, system-ui, sans-serif";
      // Left margin fits the widest Y label (e.g. "250 Mbit/s").
      this.left = 0;
      const m0 = this.scales(w, h).max;
      let lw = 0;
      for (let i = 0; i <= 4; i++) lw = Math.max(lw, ctx.measureText(fmt((m0 * i) / 4, true)).width);
      this.left = Math.ceil(lw) + 14;
      const { x, y, max, L } = this.scales(w, h);
      const grid = cssVar("--grid"), text3 = cssVar("--text-3");
      ctx.lineWidth = 1;

      // Horizontal grid + Y labels
      ctx.textAlign = "right";
      ctx.textBaseline = "middle";
      for (let i = 0; i <= 4; i++) {
        const v = (max * i) / 4;
        const yy = Math.round(y(v)) + 0.5;
        ctx.strokeStyle = grid;
        ctx.beginPath(); ctx.moveTo(L.left, yy); ctx.lineTo(w - L.right, yy); ctx.stroke();
        ctx.fillStyle = text3;
        ctx.fillText(fmt(v, true), L.left - 8, yy);
      }
      // X labels
      ctx.textAlign = "center";
      ctx.textBaseline = "top";
      const { from, to } = this.range;
      const n = Math.max(2, Math.min(6, Math.floor((w - L.left) / 90)));
      for (let i = 0; i <= n; i++) {
        const t = from + ((to - from) * i) / n;
        const xx = x(t);
        if (i === 0) ctx.textAlign = "left"; else if (i === n) ctx.textAlign = "right"; else ctx.textAlign = "center";
        ctx.fillText(this.opts.timeFormat(t, to - from), xx, h - L.bottom + 6);
      }

      // Series
      let any = false;
      const gap = this.opts.gapMs ? this.opts.gapMs(to - from) : Infinity;
      this.opts.series.forEach((s, idx) => {
        const pts = this.data[s.key] || [];
        if (pts.length) any = true;
        const color = cssVar(s.color);
        if (idx === 0 && pts.length > 1) {
          // Soft fill under the primary series.
          const grad = ctx.createLinearGradient(0, L.top, 0, h - L.bottom);
          grad.addColorStop(0, hexA(color, 0.22));
          grad.addColorStop(1, hexA(color, 0));
          ctx.fillStyle = grad;
          let start = null, prevT = null;
          ctx.beginPath();
          for (const [t, v] of pts) {
            if (prevT != null && t - prevT > gap) {
              ctx.lineTo(x(prevT), y(0)); ctx.lineTo(x(start), y(0)); ctx.closePath();
              start = null;
            }
            if (start == null) { start = t; ctx.moveTo(x(t), y(0)); }
            ctx.lineTo(x(t), y(v));
            prevT = t;
          }
          if (start != null) { ctx.lineTo(x(prevT), y(0)); ctx.lineTo(x(start), y(0)); ctx.closePath(); }
          ctx.fill();
        }
        ctx.strokeStyle = color;
        ctx.lineWidth = 1.7;
        ctx.lineJoin = "round";
        tracePath(ctx, pts, x, y, gap);
        ctx.stroke();
      });

      this.msg.textContent = this.message || (any ? "" : this.opts.emptyText);

      // Hover crosshair + tooltip
      if (this.hoverT != null && any) {
        const ref = this.opts.series.map((s) => this.data[s.key] || []).find((p) => p.length);
        const i = nearest(ref, this.hoverT);
        if (i >= 0) {
          const t = ref[i][0];
          const xx = Math.round(x(t)) + 0.5;
          ctx.strokeStyle = cssVar("--border");
          ctx.lineWidth = 1;
          ctx.beginPath(); ctx.moveTo(xx, L.top); ctx.lineTo(xx, h - L.bottom); ctx.stroke();
          const rows = [];
          for (const s of this.opts.series) {
            const pts = this.data[s.key] || [];
            const j = nearest(pts, t);
            if (j < 0 || Math.abs(pts[j][0] - t) > gap) continue;
            const color = cssVar(s.color);
            ctx.fillStyle = color;
            ctx.beginPath(); ctx.arc(x(pts[j][0]), y(pts[j][1]), 3, 0, Math.PI * 2); ctx.fill();
            rows.push({ label: s.label, color, v: fmt(pts[j][1]) });
          }
          this.showTip(xx, t, rows, w);
        }
      }
    }

    showTip(px, t, rows, w) {
      const tip = this.tip;
      tip.replaceChildren();
      const head = document.createElement("div");
      head.className = "t";
      head.textContent = this.opts.tipTimeFormat(t);
      tip.append(head);
      for (const r of rows) {
        const row = document.createElement("div");
        row.className = "r";
        const name = document.createElement("span");
        const sw = document.createElement("i");
        sw.style.background = r.color;
        name.append(sw, document.createTextNode(r.label));
        const val = document.createElement("b");
        val.textContent = r.v;
        row.append(name, val);
        tip.append(row);
      }
      tip.hidden = false;
      const tw = tip.offsetWidth;
      tip.style.left = (px + 12 + tw > w ? px - tw - 12 : px + 12) + "px";
      tip.style.top = "6px";
    }

    onMove(e) {
      if (!this.range || !e) return;
      const rect = this.canvas.getBoundingClientRect();
      const { t } = this.scales(rect.width, rect.height);
      this.hoverT = t(e.clientX - rect.left);
      this.draw();
    }
  }

  // Sparkline: tiny line, no axes. values: array of numbers (or null).
  function sparkline(canvas, values, colorVar, max) {
    const { ctx, w, h } = setupCanvas(canvas);
    const vals = values.filter((v) => v != null);
    if (vals.length < 2) return;
    const m = max != null ? max : Math.max(1, ...vals) * 1.1;
    const color = cssVar(colorVar || "--accent");
    const step = w / (values.length - 1);
    ctx.strokeStyle = color;
    ctx.lineWidth = 1.5;
    ctx.lineJoin = "round";
    ctx.beginPath();
    let started = false;
    values.forEach((v, i) => {
      if (v == null) { started = false; return; }
      const xx = i * step, yy = h - 2 - (Math.min(v, m) / m) * (h - 4);
      if (!started) { ctx.moveTo(xx, yy); started = true; } else ctx.lineTo(xx, yy);
    });
    ctx.stroke();
  }

  function hexA(color, a) {
    const m = /^#([0-9a-f]{6})$/i.exec(color);
    if (!m) return color;
    const n = parseInt(m[1], 16);
    return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${a})`;
  }

  global.MChart = { LineChart, sparkline, cssVar };
})(window);
