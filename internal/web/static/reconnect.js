// Wait-page reconnect: after an initial delay, poll until the server answers,
// then navigate home. Keeps written instructions visible the whole time.
(function () {
  "use strict";
  var delay = parseInt(document.body.dataset.reconnectDelay || "5000", 10);
  function poll() {
    fetch("/", { method: "HEAD", cache: "no-store" })
      .then(function (r) { if (r.ok || r.status === 401 || r.status === 302) { window.location.href = "/"; } })
      .catch(function () { /* still down; keep polling */ })
      .finally(function () { setTimeout(poll, 2000); });
  }
  setTimeout(poll, delay);
})();
