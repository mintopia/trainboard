// Live board renderer: polls /api/board and renders the panel content as DOM.
// Replaces the old 1fps /preview.png poll (PNG encode on the Pi per second).
// All content set via textContent — never innerHTML — the data is remote text.
(function () {
  "use strict";
  var root = document.getElementById("board");
  if (!root) return;
  var caption = document.getElementById("board-caption");
  var endpoint = root.dataset.endpoint || "/api/board";
  var last = "";

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text) e.textContent = text;
    return e;
  }

  function row(parts) { // parts: array of [cls, text]
    var r = el("div", "row");
    parts.forEach(function (p) { r.appendChild(el("span", p[0], p[1])); });
    return r;
  }

  function render(v) {
    var key = JSON.stringify(v);
    if (key === last) return; // don't restart the marquee for identical data
    last = key;
    root.textContent = "";

    if (v.state === "departures" && v.first) {
      root.appendChild(row([["", v.first.order + " " + v.first.scheduled + " "],
        ["dest", v.first.destination],
        ["", (v.first.platform ? "Plat " + v.first.platform + "  " : "") + v.first.status]]));
      var clip = el("div", "marquee-clip");
      clip.appendChild(el("span", "marquee",
        "Calling at: " + (v.first.callingAt || "") +
        (v.first.serviceInfo ? "   ·   " + v.first.serviceInfo : "")));
      root.appendChild(clip);
      (v.remaining || []).slice(0, 1).forEach(function (s) {
        root.appendChild(row([["", s.order + " " + s.scheduled + " "], ["dest", s.destination], ["", s.status]]));
      });
    } else if (v.state === "hotspot" && v.hotspot) {
      root.appendChild(row([["dest center", "Setup mode"]]));
      root.appendChild(row([["dest center", "Join hotspot: " + v.hotspot.ssid]]));
      root.appendChild(row([["dest center", "Then open http://" + v.hotspot.addr]]));
    } else if (v.state === "no-services") {
      root.appendChild(row([["dest center", v.location || ""]]));
      root.appendChild(row([["dest center", (v.messages && v.messages[0]) || "No services to show"]]));
    } else {
      root.appendChild(row([["dest center", v.message || v.state]]));
    }
    var clock = el("div", "clockline", "");
    clock.id = "board-clock";
    root.appendChild(clock);
    tickClock();
  }

  function tickClock() {
    var c = document.getElementById("board-clock");
    if (!c) return;
    c.textContent = new Date().toLocaleTimeString("en-GB", { timeZone: "Europe/London", hour12: false });
  }

  function staleness(v) {
    if (!caption) return;
    if (!v.fetchedAt) { caption.textContent = "Live panel · rendered in your browser"; return; }
    var age = Math.max(0, (Date.now() - Date.parse(v.fetchedAt)) / 1000);
    if (age > 300) {
      root.classList.add("stale");
      caption.textContent = "Live panel · data " + Math.round(age / 60) + " min old";
    } else {
      root.classList.remove("stale");
      caption.textContent = "Live panel · data " + Math.round(age) + "s old";
    }
  }

  function poll() {
    fetch(endpoint, { headers: { Accept: "application/json" } })
      .then(function (r) {
        if (r.status === 401) { window.location.href = "/login"; throw new Error("unauthenticated"); }
        return r.json();
      })
      .then(function (v) { render(v); staleness(v); })
      .catch(function () { /* transient; next poll retries */ });
  }

  poll();
  setInterval(poll, 5000);
  setInterval(tickClock, 1000);
})();
