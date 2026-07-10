// suggest.js — dependency-free accessible autosuggest (#62, #63).
// Enhances <input data-suggest="<endpoint>"> into a WAI-ARIA combobox.
//   data-hint="<id>"   element that shows the resolved name(s)
//   data-multi=","     comma-separated multi-token field (operators)
// Endpoints return JSON arrays of {crs|code, name}. Selection writes the
// CODE into the input — the server contract is unchanged, and without JS
// the field remains a plain code input. textContent only, never innerHTML.
(function () {
  "use strict";
  var seq = 0;
  document.querySelectorAll("input[data-suggest]").forEach(enhance);

  function enhance(input) {
    var endpoint = input.dataset.suggest;
    var multi = input.dataset.multi || "";
    var hint = input.dataset.hint ? document.getElementById(input.dataset.hint) : null;
    var cache = null; // full-list cache (TOCs); stations always query

    // Free typing needs room: suspend 3-char constraints; the server still
    // validates codes on submit.
    input.removeAttribute("maxlength");
    input.removeAttribute("pattern");
    input.setAttribute("role", "combobox");
    input.setAttribute("aria-autocomplete", "list");
    input.setAttribute("aria-expanded", "false");
    input.autocomplete = "off";

    var box = document.createElement("div");
    box.className = "suggestwrap";
    input.parentNode.insertBefore(box, input);
    box.appendChild(input);
    var list = document.createElement("ul");
    list.className = "suggest";
    list.id = "suggest-" + (++seq);
    list.setAttribute("role", "listbox");
    list.hidden = true;
    box.appendChild(list);
    input.setAttribute("aria-controls", list.id);

    var items = [], active = -1, timer = null;

    function token() {
      if (!multi) return input.value.trim();
      var parts = input.value.split(multi);
      return parts[parts.length - 1].trim();
    }

    function close() {
      list.hidden = true;
      input.setAttribute("aria-expanded", "false");
      input.removeAttribute("aria-activedescendant");
      items = []; active = -1;
      input.classList.remove("searching");
    }

    function show(results) {
      list.textContent = "";
      items = results; active = -1;
      if (!results.length) { close(); return; }
      results.forEach(function (s, i) {
        var li = document.createElement("li");
        li.id = list.id + "-" + i;
        li.setAttribute("role", "option");
        li.textContent = s.name + " (" + (s.crs || s.code) + ")";
        li.addEventListener("mousedown", function (ev) { ev.preventDefault(); pick(i); });
        list.appendChild(li);
      });
      list.hidden = false;
      input.setAttribute("aria-expanded", "true");
    }

    function highlight(i) {
      var opts = list.children;
      if (active >= 0 && opts[active]) opts[active].removeAttribute("aria-selected");
      active = i;
      if (i >= 0 && opts[i]) {
        opts[i].setAttribute("aria-selected", "true");
        input.setAttribute("aria-activedescendant", opts[i].id);
      } else {
        input.removeAttribute("aria-activedescendant");
      }
    }

    function pick(i) {
      var s = items[i];
      if (!s) return;
      var code = (s.crs || s.code).toUpperCase();
      if (multi) {
        var parts = input.value.split(multi);
        parts[parts.length - 1] = " " + code;
        input.value = parts.join(multi).replace(/^ /, "");
      } else {
        input.value = code;
        if (hint) hint.textContent = s.name;
      }
      close();
      if (multi && hint) resolveMulti();
      input.dispatchEvent(new Event("change", { bubbles: true }));
      input.focus();
    }

    // Multi hint: resolve every token against the cached full table:
    // "GW, XR" → "Great Western Railway, Elizabeth line".
    function resolveMulti() {
      fullList().then(function (all) {
        var names = [];
        input.value.split(multi).forEach(function (t) {
          t = t.trim().toUpperCase();
          if (!t) return;
          var m = all.filter(function (s) { return (s.code || s.crs) === t; })[0];
          names.push(m ? m.name : t + "?");
        });
        hint.textContent = names.join(", ");
      });
    }

    function fullList() {
      if (cache) return Promise.resolve(cache);
      return fetch(endpoint, { headers: { Accept: "application/json" } })
        .then(function (r) { return r.json(); })
        .then(function (v) { cache = v; return v; });
    }

    function search() {
      var q = token();
      if (q.length < 2) { close(); return; }
      fetch(endpoint + "?q=" + encodeURIComponent(q), { headers: { Accept: "application/json" } })
        .then(function (r) { return r.json(); })
        .then(function (v) {
          if (token() !== q) return; // stale response
          show(v.slice(0, 8));
          // Exact code typed by hand: keep the hint honest without a pick.
          if (!multi && hint) {
            var exact = v.filter(function (s) { return (s.crs || s.code) === q.toUpperCase(); })[0];
            if (exact) hint.textContent = exact.name;
          }
        })
        .catch(close);
    }

    input.addEventListener("input", function () {
      input.classList.add("searching");
      clearTimeout(timer);
      timer = setTimeout(search, 250);
      if (multi && hint) { clearTimeout(input._mt); input._mt = setTimeout(resolveMulti, 600); }
    });

    input.addEventListener("keydown", function (ev) {
      if (list.hidden) return;
      if (ev.key === "ArrowDown") { ev.preventDefault(); highlight(Math.min(active + 1, items.length - 1)); }
      else if (ev.key === "ArrowUp") { ev.preventDefault(); highlight(Math.max(active - 1, 0)); }
      else if (ev.key === "Enter") { if (active >= 0) { ev.preventDefault(); pick(active); } }
      else if (ev.key === "Escape") { close(); }
    });

    input.addEventListener("blur", function () { setTimeout(close, 150); });

    if (multi && hint && input.value.trim()) resolveMulti();
  }
})();
