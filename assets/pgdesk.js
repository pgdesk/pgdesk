// pgdesk progressive enhancement. Ships as an external, CSP-clean file (F2):
// no inline handlers, no eval. The admin is fully functional without it.
(function () {
  "use strict";

  // --- Confirm destructive actions before submitting -----------------------
  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (form && form.matches("[data-confirm]")) {
      if (!window.confirm(form.getAttribute("data-confirm"))) {
        e.preventDefault();
      }
    }
  });

  // --- Warn on navigating away from a form with unsaved changes ------------
  document.querySelectorAll("form.pg-form").forEach(function (form) {
    var dirty = false;
    form.addEventListener("input", function () { dirty = true; });
    form.addEventListener("submit", function () { dirty = false; });
    window.addEventListener("beforeunload", function (e) {
      if (dirty) { e.preventDefault(); e.returnValue = ""; }
    });
  });

  // --- Bulk-action helpers -------------------------------------------------
  // Select-all toggles every row checkbox in the same table.
  document.querySelectorAll("[data-select-all]").forEach(function (master) {
    master.addEventListener("change", function () {
      var table = master.closest("table");
      if (!table) return;
      table.querySelectorAll('input[type="checkbox"][name="key"]').forEach(function (cb) {
        cb.checked = master.checked;
      });
    });
  });
  // The action form confirms using the selected action's data-confirm text.
  document.querySelectorAll("form.pg-action-form").forEach(function (form) {
    form.addEventListener("submit", function (e) {
      var sel = form.querySelector('select[name="_action"]');
      if (!sel || !sel.value) { e.preventDefault(); return; }
      var opt = sel.options[sel.selectedIndex];
      var confirmText = opt && opt.getAttribute("data-confirm");
      if (confirmText && !window.confirm(confirmText)) { e.preventDefault(); }
    });
  });

  // --- Theme toggle (persisted; the FOUC-free initial set is inline) --------
  function currentTheme() {
    return document.documentElement.getAttribute("data-theme") ||
      (window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
  }
  document.querySelectorAll("[data-theme-toggle]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var next = currentTheme() === "dark" ? "light" : "dark";
      document.documentElement.setAttribute("data-theme", next);
      try { localStorage.setItem("pgdesk-theme", next); } catch (e) { /* ignore */ }
    });
  });

  // --- Keyboard shortcuts ---------------------------------------------------
  function isTyping(el) {
    return el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" ||
      el.tagName === "SELECT" || el.isContentEditable);
  }
  document.addEventListener("keydown", function (e) {
    if (e.ctrlKey || e.metaKey || e.altKey || isTyping(e.target)) return;
    if (e.key === "/") {
      var search = document.querySelector('input[type="search"]');
      if (search) { e.preventDefault(); search.focus(); }
    } else if (e.key === "c") {
      var add = document.querySelector('.pg-list-header a[href$="/new"]');
      if (add) { e.preventDefault(); window.location.href = add.getAttribute("href"); }
    } else if (e.key === "e") {
      var edit = document.querySelector('.pg-detail-actions a[href$="/edit"]');
      if (edit) { e.preventDefault(); window.location.href = edit.getAttribute("href"); }
    }
  });
})();
