// Live board renderer: polls /api/board and emulates the SSD1322 panel.
// Fidelity contract (#61): geometry, fonts, and tick timing are copied from
// the panel renderer — constants cite their Go sources and MUST track them:
//   internal/board/board.go        (pixel geometry)
//   internal/board/elements.go     (slide-in + remaining-services roll)
//   internal/render/element_scrollingtext.go (scroll cycle)
//   internal/board/fonts.go, internal/render/element_clock.go (fonts, clock)
// All content set via textContent — never innerHTML — the data is remote text.
(function () {
  "use strict";
  var root = document.getElementById("board");
  if (!root) return;
  var caption = document.getElementById("board-caption");
  var endpoint = root.dataset.endpoint || "/api/board";
  var reduced = window.matchMedia("(prefers-reduced-motion: reduce)");

  // --- Panel geometry (internal/board/board.go) ---
  var W = 256, ROW_H = 12;
  var COL_ORDER_X = 0, COL_SCHED_X = 17, COL_SCHED_W = 28;
  var COL_PLAT_X = 45, COL_PLAT_W = 19, COL_DEST_X = 64;
  var COL_HC_X = 45, COL_HC_W = 27;    // board.go ColHeadcodeX/W (layout.headcodes)
  var COL_STATUS_X = 216, COL_STATUS_W = 40;
  var CALLING_LABEL_W = 42, CALLING_X = 42, CALLING_W = 214;
  var SERVICE_Y = 24, REMAINING_Y = 36, CLOCK_Y = 50;
  // --- Timing (internal/board/elements.go; 1 tick = 40ms) ---
  var TICK_MS = 40;
  var NS_STEP = 2;                       // next-service slide px/tick
  var RS_STEP = 2, RS_PAUSE = 125;       // remaining-services roll
  var RS_MOVE = ROW_H / RS_STEP;         // 6 ticks
  var RS_SEG = RS_PAUSE + RS_MOVE;       // 131 ticks
  var SCROLL_PAUSE = 60;                 // element_scrollingtext.go

  var FONT_REG = '10px "Dot Matrix", monospace';
  var FONT_CLOCK = '20px "Dot Matrix Bold", monospace';
  var FONT_CLOCK_SEC = '10px "Dot Matrix Bold Tall", monospace';

  var measureCtx = document.createElement("canvas").getContext("2d");
  function textW(text, font) {
    measureCtx.font = font || FONT_REG;
    return Math.ceil(measureCtx.measureText(text).width);
  }

  // --- Stage & scaling ---
  var stage = document.createElement("div");
  stage.className = "board-stage";
  root.appendChild(stage);
  function rescale() {
    stage.style.transform = "scale(" + root.clientWidth / W + ")";
  }
  if (window.ResizeObserver) new ResizeObserver(rescale).observe(root);
  window.addEventListener("resize", rescale);
  rescale();

  function el(tag, cls) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    return e;
  }
  function place(e, x, y, w, h) {
    e.style.left = x + "px"; e.style.top = y + "px";
    e.style.width = w + "px"; e.style.height = h + "px";
    return e;
  }

  // staticText mirrors render.StaticText: text drawn once inside a box with
  // left/center/right alignment, clipped.
  function staticText(parent, text, x, y, w, align, font) {
    var clip = place(el("div", "clip"), x, y, w, ROW_H);
    var t = el("span", "t");
    t.textContent = text;
    if (font) t.style.font = font;
    var tw = textW(text, font);
    var dx = align === "center" ? Math.max(0, (w - tw) >> 1)
           : align === "right" ? Math.max(0, w - tw) : 0;
    t.style.left = dx + "px"; t.style.top = "0";
    clip.appendChild(t);
    parent.appendChild(clip);
  }

  // scrollOffset mirrors render.scrollOffset exactly: static while the text
  // fits; else hold SCROLL_PAUSE, travel 1px/tick until fully out (tw),
  // hold blank SCROLL_PAUSE, wrap.
  function scrollOffset(tw, boxW, tick) {
    if (tw <= boxW) return 0;
    var cycle = SCROLL_PAUSE + tw + SCROLL_PAUSE;
    var t = tick % cycle;
    if (t < SCROLL_PAUSE) return 0;
    var off = t - SCROLL_PAUSE;
    return off < tw ? off : tw;
  }

  // Animated elements register an update(tick) here; one rAF loop drives all.
  var animated = [];
  // Epochs preserve each element's animation phase across identical redraws:
  // key -> {text, t0}. A changed text restarts only that element (#61).
  var epochs = {};
  function epoch(key, text, now) {
    var e = epochs[key];
    if (!e || e.text !== text) e = epochs[key] = { text: text, t0: now };
    return e.t0;
  }

  function scrollingText(parent, key, text, x, y, w, now) {
    var clip = place(el("div", "clip"), x, y, w, ROW_H);
    var t = el("span", "t");
    t.textContent = text;
    t.style.left = "0"; t.style.top = "0";
    clip.appendChild(t);
    parent.appendChild(clip);
    var tw = textW(text);
    if (tw <= w || reduced.matches) return; // static (panel truncates only by clip)
    var t0 = epoch(key, text, now);
    animated.push(function (nowMs) {
      var tick = Math.floor((nowMs - t0) / TICK_MS);
      t.style.transform = "translateX(" + -scrollOffset(tw, w, tick) + "px)";
    });
  }

  // rowInto mirrors board.rowElements: departure row, with the optional
  // headcode column (layout.headcodes) between sched and platform.
  function rowInto(parent, s, y) {
    staticText(parent, s.order + ordSuffix(s.order), COL_ORDER_X, y, COL_SCHED_X, "left");
    staticText(parent, s.scheduled, COL_SCHED_X, y, COL_SCHED_W, "center");
    var shift = hcOn ? COL_HC_W : 0;
    if (hcOn && s.headcode) staticText(parent, s.headcode, COL_HC_X, y, COL_HC_W, "center");
    if (s.platform) staticText(parent, s.platform, COL_PLAT_X + shift, y, COL_PLAT_W, "center");
    staticText(parent, s.destination, COL_DEST_X + shift, y, COL_STATUS_X - COL_DEST_X - shift, "left");
    staticText(parent, s.status, COL_STATUS_X, y, COL_STATUS_W, "right");
  }
  // Server sends order as an int; suffix mirrors board.Ordinal.
  function ordSuffix(n) {
    var m = n % 100;
    if (m >= 11 && m <= 13) return "th";
    switch (n % 10) { case 1: return "st"; case 2: return "nd"; case 3: return "rd"; }
    return "th";
  }

  // nextServiceRow mirrors board.nextServiceRow: the first row slides up
  // from the bottom of its 12px band at 2px/tick, then holds.
  function nextServiceRow(parent, s, now) {
    var clip = place(el("div", "clip"), 0, 0, W, ROW_H);
    var strip = place(el("div", "t"), 0, 0, W, ROW_H);
    strip.style.position = "absolute";
    rowInto(strip, s, 0);
    clip.appendChild(strip);
    parent.appendChild(clip);
    if (reduced.matches) return;
    var key = "first";
    var text = JSON.stringify([s.order, s.scheduled, s.destination, s.platform, s.headcode, s.status]);
    var t0 = epoch(key, text, now);
    animated.push(function (nowMs) {
      var tick = Math.floor((nowMs - t0) / TICK_MS);
      var b = Math.min(NS_STEP * (tick + 1), ROW_H);
      strip.style.transform = "translateY(" + (ROW_H - b) + "px)";
    });
  }

  // remainingBand mirrors board.remainingServices: a strip of
  // [blank, svc2..svcN, dup-svc2] rows rolling vertically in a 12px window —
  // slide in (6 ticks), then per segment move 12px over 6 ticks and hold 5s,
  // wrapping seamlessly via the duplicated row.
  function remainingBand(parent, deps, now) {
    if (!deps.length) return;
    var n = deps.length;
    var clip = place(el("div", "clip"), 0, REMAINING_Y, W, ROW_H);
    var strip = el("div", "t");
    strip.style.position = "absolute"; strip.style.left = "0"; strip.style.top = "0";
    strip.style.width = W + "px"; strip.style.height = (n + 2) * ROW_H + "px";
    deps.forEach(function (s, i) { rowInto(strip, s, (i + 1) * ROW_H); });
    rowInto(strip, deps[0], (n + 1) * ROW_H); // dup covers mid-move wrap
    clip.appendChild(strip);
    parent.appendChild(clip);
    if (reduced.matches) {              // static: show the 2nd service, no roll
      strip.style.transform = "translateY(" + -ROW_H + "px)";
      return;
    }
    var key = "remaining";
    var text = JSON.stringify(deps);
    var t0 = epoch(key, text, now);
    animated.push(function (nowMs) {
      var tick = Math.floor((nowMs - t0) / TICK_MS);
      var ty;
      if (tick < RS_MOVE) {             // slide-in: strip top at band bottom
        ty = ROW_H - RS_STEP * (tick + 1);
      } else {                          // move-then-hold cycle
        var t = tick - RS_MOVE, s = Math.floor(t / RS_SEG), w = t % RS_SEG;
        var step = Math.min(RS_STEP * (w + 1), ROW_H);
        var u = ROW_H * s + step;
        var top = u > ROW_H ? ROW_H + (u - ROW_H) % (ROW_H * n) : u;
        ty = -top;
      }
      strip.style.transform = "translateY(" + ty + "px)";
    });
  }

  // clock mirrors render.Clock: Bold 20px HH:MM + Bold Tall 10px :SS at a
  // 5px drop, the pair centered. Driven from BOARD time (Task 2), not the
  // browser clock — drift between them is surfaced in the caption (#65).
  var clockBase = null, clockAt = 0; // server epoch ms, and Date.now() at fetch
  function clockText() {
    var d = clockBase === null ? new Date() : new Date(clockBase + (Date.now() - clockAt));
    var s = d.toLocaleTimeString("en-GB", { timeZone: "Europe/London", hour12: false });
    return [s.slice(0, 5), s.slice(5, 8)]; // ["HH:MM", ":SS"]
  }
  var clockHM = null, clockSS = null;
  function clockInto(parent) {
    var parts = clockText();
    var w1 = textW(parts[0], FONT_CLOCK), w2 = textW(parts[1], FONT_CLOCK_SEC);
    var margin = Math.max(0, (W - (w1 + w2)) >> 1);
    clockHM = el("span", "t"); clockHM.style.font = FONT_CLOCK;
    clockHM.style.left = margin + "px"; clockHM.style.top = CLOCK_Y + "px";
    clockHM.style.lineHeight = "14px";
    clockSS = el("span", "t"); clockSS.style.font = FONT_CLOCK_SEC;
    clockSS.style.left = (margin + w1) + "px";
    clockSS.style.top = (CLOCK_Y + 5) + "px"; // clockSecondsDrop=5
    parent.appendChild(clockHM); parent.appendChild(clockSS);
    tickClock();
  }
  function tickClock() {
    if (!clockHM) return;
    var parts = clockText();
    clockHM.textContent = parts[0];
    clockSS.textContent = parts[1];
  }

  function centeredLine(parent, text, y) {
    staticText(parent, text, 0, y, W, "center");
  }

  var last = "";
  var hcOn = false; // layout.headcodes, from the last /api/board payload
  function render(v) {
    var key = JSON.stringify(v, function (k, val) { return k === "time" ? undefined : val; });
    if (key === last) return; // identical scene: leave animations untouched
    last = key;
    animated = [];
    stage.textContent = "";
    var now = performance.now();
    hcOn = !!v.headcodes;

    if (v.state === "departures" && v.first) {
      nextServiceRow(stage, v.first, now);
      // The panel's "Calling at:" label is a ScrollingText (scene_departures.go:50),
      // but it is static in practice: the Dot Matrix bitmap text fits its 42px
      // box exactly. Browser canvas measureText overestimates the webfont's
      // width, so mirroring it as a ScrollingText here made it scroll on the
      // web when the glass never does (#67) — rendered explicitly static
      // instead, to match what's actually on the panel.
      staticText(stage, "Calling at:", 0, ROW_H, CALLING_LABEL_W, "left");
      scrollingText(stage, "calling-list", v.first.callingAt || "", CALLING_X, ROW_H, CALLING_W, now);
      scrollingText(stage, "service-info", v.first.serviceInfo || "", 0, SERVICE_Y, W, now);
      remainingBand(stage, v.remaining || [], now);
    } else if (v.state === "hotspot" && v.hotspot) {
      centeredLine(stage, "Setup mode", 0);
      centeredLine(stage, "Join hotspot: " + v.hotspot.ssid, ROW_H);
      centeredLine(stage, "Then open http://" + v.hotspot.addr, SERVICE_Y);
    } else if (v.state === "no-services") {
      centeredLine(stage, v.location || "", 0);
      centeredLine(stage, (v.messages && v.messages[0]) || "No services to show", ROW_H);
    } else {
      centeredLine(stage, v.message || v.state, ROW_H);
    }
    clockInto(stage);
  }

  function staleness(v) {
    if (!caption) return;
    var text;
    if (!v.fetchedAt) {
      root.classList.remove("stale");
      text = "Live panel · rendered in your browser";
    } else {
      var age = Math.max(0, (Date.now() - Date.parse(v.fetchedAt)) / 1000);
      if (age > 300) {
        root.classList.add("stale");
        text = "Live panel · data " + Math.round(age / 60) + " min old";
      } else {
        root.classList.remove("stale");
        text = "Live panel · data " + Math.round(age) + "s old";
      }
    }
    if (clockBase !== null) {
      var drift = Math.round(Math.abs(Date.now() - (clockBase + (Date.now() - clockAt))) / 1000);
      if (drift > 30) text += " · board clock differs from this device by " + drift + "s";
    }
    caption.textContent = text;
  }

  function poll() {
    fetch(endpoint, { headers: { Accept: "application/json" } })
      .then(function (r) {
        if (r.status === 401) { window.location.href = "/login"; throw new Error("unauthenticated"); }
        return r.json();
      })
      .then(function (v) {
        if (v.time) { clockBase = Date.parse(v.time); clockAt = Date.now(); }
        render(v); staleness(v);
      })
      .catch(function () { /* transient; next poll retries */ });
  }

  function frame(nowMs) {
    if (!document.hidden) {
      for (var i = 0; i < animated.length; i++) animated[i](nowMs);
    }
    requestAnimationFrame(frame);
  }

  // Fonts affect measurement (scroll distances, clock centering): render
  // once immediately, then re-render after the faces load.
  poll();
  setInterval(poll, 5000);
  setInterval(tickClock, 1000);
  if (!reduced.matches) requestAnimationFrame(frame);
  if (document.fonts && document.fonts.ready) {
    document.fonts.ready.then(function () { last = ""; poll(); });
  }
})();
