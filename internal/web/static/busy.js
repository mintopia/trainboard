// Busy states for slow form posts (updates): any form with a data-busy
// attribute disables every update button on submit and shows the message
// with a spinner until the server responds (PRG navigation replaces the
// page, so there is nothing to undo on success; a failed network submit
// re-enables via pageshow when the user navigates back). The notice is
// inserted before the form's enclosing .btnrow (if any) so it renders as a
// full-width banner instead of being squeezed into the flex button row.
(function () {
  "use strict";
  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!form.hasAttribute || !form.hasAttribute("data-busy")) return;
    var msg = form.getAttribute("data-busy");
    document.querySelectorAll("form[data-busy] button").forEach(function (b) {
      b.disabled = true;
    });
    var n = document.createElement("div");
    n.className = "notice calm busy";
    var spin = document.createElement("span");
    spin.className = "spinner";
    n.appendChild(spin);
    n.appendChild(document.createTextNode(" " + msg));
    var anchor = form.closest(".btnrow") || form;
    anchor.parentNode.insertBefore(n, anchor);
  });
  // bfcache restore (user pressed Back mid-update): reset the buttons.
  window.addEventListener("pageshow", function (e) {
    if (!e.persisted) return;
    document.querySelectorAll("form[data-busy] button").forEach(function (b) {
      b.disabled = false;
    });
    document.querySelectorAll(".notice.busy").forEach(function (n) { n.remove(); });
  });
})();
