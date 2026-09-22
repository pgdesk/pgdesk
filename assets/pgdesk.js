(function () {
  "use strict";

  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (form && form.matches("[data-confirm]")) {
      if (!window.confirm(form.getAttribute("data-confirm"))) {
        e.preventDefault();
      }
    }
  });

  document.querySelectorAll("form.pg-form").forEach(function (form) {
    var dirty = false;
    form.addEventListener("input", function () { dirty = true; });
    form.addEventListener("change", function () { dirty = true; });
    form.addEventListener("submit", function () {
      dirty = false;
      var btn = form.querySelector('button[type="submit"]');
      if (btn) { btn.setAttribute("aria-busy", "true"); btn.classList.add("pg-busy"); }
    });
    window.addEventListener("beforeunload", function (e) {
      if (dirty) { e.preventDefault(); e.returnValue = ""; }
    });
  });

  document.querySelectorAll(".pg-switch input[type=checkbox]").forEach(function (cb) {
    var text = cb.parentElement.querySelector("span");
    cb.addEventListener("change", function () { if (text) text.textContent = cb.checked ? "Yes" : "No"; });
  });

  document.querySelectorAll("[data-select-all]").forEach(function (master) {
    master.addEventListener("change", function () {
      var table = master.closest("table");
      if (!table) return;
      table.querySelectorAll('input[type="checkbox"][name="key"]').forEach(function (cb) {
        cb.checked = master.checked;
      });
    });
  });
  document.querySelectorAll("form.pg-action-form").forEach(function (form) {
    form.addEventListener("submit", function (e) {
      var sel = form.querySelector('select[name="_action"]');
      if (!sel || !sel.value) { e.preventDefault(); return; }
      var opt = sel.options[sel.selectedIndex];
      var confirmText = opt && opt.getAttribute("data-confirm");
      if (confirmText && !window.confirm(confirmText)) { e.preventDefault(); }
    });
  });

  document.querySelectorAll("tr[data-pg-row-href]").forEach(function (row) {
    row.addEventListener("click", function (e) {
      if (e.target.closest("a, button, input, select, label, textarea")) return;
      if (window.getSelection && String(window.getSelection())) return;
      var href = row.getAttribute("data-pg-row-href");
      if (e.metaKey || e.ctrlKey) { window.open(href, "_blank"); } else { window.location.href = href; }
    });
  });

  function currentTheme() {
    return document.documentElement.getAttribute("data-theme") ||
      (window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
  }
  document.querySelectorAll("[data-theme-toggle]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var next = currentTheme() === "dark" ? "light" : "dark";
      document.documentElement.setAttribute("data-theme", next);
      try { localStorage.setItem("pgdesk-theme", next); } catch (e) {  }
    });
  });

  var navToggle = document.querySelector("[data-pg-nav-toggle]");
  if (navToggle) {
    navToggle.addEventListener("click", function () {
      var open = document.body.classList.toggle("pg-nav-open");
      navToggle.setAttribute("aria-expanded", open ? "true" : "false");
    });
  }

  function fold(s) { return (s || "").toLowerCase().normalize("NFKD").replace(/[̀-ͯ]/g, ""); }

  var navFilter = document.querySelector("[data-pg-nav-filter]");
  if (navFilter) {
    var groups = document.querySelectorAll("[data-pg-nav-group]");
    var empty = document.querySelector("[data-pg-nav-empty]");
    var applyFilter = function () {
      var q = fold(navFilter.value.trim());
      var shown = 0;
      groups.forEach(function (g) {
        var inGroup = 0;
        g.querySelectorAll("[data-pg-nav-item]").forEach(function (a) {
          var hit = !q || fold(a.textContent).indexOf(q) !== -1 || fold(a.getAttribute("href")).indexOf(q) !== -1;
          a.parentElement.hidden = !hit;
          if (hit) inGroup++;
        });
        g.hidden = inGroup === 0;
        shown += inGroup;
      });
      if (empty) empty.hidden = shown !== 0;
    };
    navFilter.addEventListener("input", applyFilter);
    navFilter.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        var first = document.querySelector("[data-pg-nav-group]:not([hidden]) li:not([hidden]) a");
        if (first) { e.preventDefault(); window.location.href = first.getAttribute("href"); }
      } else if (e.key === "Escape") {
        navFilter.value = ""; applyFilter(); navFilter.blur();
      }
    });
    var active = document.querySelector(".pg-nav-active");
    if (active && active.scrollIntoView) active.scrollIntoView({ block: "center" });
  }

  document.querySelectorAll(".pg-fk[data-pg-fk-url]").forEach(function (wrap) {
    var input = wrap.querySelector(".pg-fk-search");
    var hidden = wrap.querySelector("[data-pg-fk-value]");
    var keyEl = wrap.querySelector("[data-pg-fk-key]");
    var clearBtn = wrap.querySelector("[data-pg-fk-clear]");
    var url = wrap.getAttribute("data-pg-fk-url");
    if (!input || !hidden || !url) return;

    var chosenLabel = input.value;
    var listbox = document.createElement("ul");
    listbox.className = "pg-fk-listbox";
    listbox.id = "pg-fk-list-" + input.id;
    listbox.setAttribute("role", "listbox");
    listbox.hidden = true;
    wrap.appendChild(listbox);

    input.setAttribute("role", "combobox");
    input.setAttribute("aria-expanded", "false");
    input.setAttribute("aria-controls", listbox.id);
    input.setAttribute("aria-autocomplete", "list");

    var options = [];
    var active = -1;
    var timer = null;
    var seq = 0;

    function close() {
      listbox.hidden = true;
      input.setAttribute("aria-expanded", "false");
      input.removeAttribute("aria-activedescendant");
      active = -1;
    }

    function setValue(value, label) {
      hidden.value = value;
      chosenLabel = label;
      input.value = label;
      if (keyEl) keyEl.textContent = value;
      if (clearBtn) clearBtn.hidden = value === "";
      hidden.dispatchEvent(new Event("change", { bubbles: true }));
    }

    function highlight(i) {
      var items = listbox.querySelectorAll(".pg-fk-option");
      if (!items.length) return;
      if (i < 0) i = items.length - 1;
      if (i >= items.length) i = 0;
      active = i;
      items.forEach(function (el, n) {
        el.setAttribute("aria-selected", n === i ? "true" : "false");
      });
      input.setAttribute("aria-activedescendant", items[i].id);
      items[i].scrollIntoView({ block: "nearest" });
    }

    function choose(i) {
      if (!options[i]) return;
      setValue(String(options[i].value), options[i].label);
      close();
    }

    function note(text) {
      var li = document.createElement("li");
      li.className = "pg-fk-note";
      li.textContent = text;
      listbox.appendChild(li);
    }

    function render(resp) {
      options = (resp && resp.options) || [];
      listbox.textContent = "";
      if (!options.length) note("No matches.");
      options.forEach(function (opt, i) {
        var li = document.createElement("li");
        li.className = "pg-fk-option";
        li.id = listbox.id + "-" + i;
        li.setAttribute("role", "option");
        li.setAttribute("aria-selected", String(opt.value) === hidden.value ? "true" : "false");
        var label = document.createElement("span");
        label.className = "pg-fk-option-label";
        label.textContent = opt.label;
        var key = document.createElement("span");
        key.className = "pg-fk-option-key";
        key.textContent = opt.value;
        li.appendChild(label);
        li.appendChild(key);
        li.addEventListener("mousedown", function (e) {
          e.preventDefault();
          choose(i);
        });
        listbox.appendChild(li);
      });
      if (resp && resp.searchable === false) {
        note("This table can't be searched; showing the first rows.");
      } else if (resp && resp.truncated) {
        note("More matches exist. Keep typing to narrow them down.");
      }
      listbox.hidden = false;
      input.setAttribute("aria-expanded", "true");
      active = -1;
    }

    function search(term) {
      var mine = ++seq;
      listbox.setAttribute("aria-busy", "true");
      fetch(url + "?q=" + encodeURIComponent(term), {
        credentials: "same-origin",
        headers: { "Accept": "application/json" }
      }).then(function (res) {
        if (!res.ok) return null;
        return res.json();
      }).then(function (data) {
        if (mine !== seq) return;
        listbox.removeAttribute("aria-busy");
        if (data) render(data); else close();
      }).catch(function () { close(); });
    }

    input.addEventListener("focus", function () {
      input.select();
      search(input.value === chosenLabel ? "" : input.value);
    });
    input.addEventListener("input", function () {
      window.clearTimeout(timer);
      timer = window.setTimeout(function () { search(input.value); }, 180);
    });
    input.addEventListener("keydown", function (e) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        if (listbox.hidden) search(input.value); else highlight(active + 1);
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        if (!listbox.hidden) highlight(active - 1);
      } else if (e.key === "Enter") {
        if (!listbox.hidden) {
          e.preventDefault();
          if (active >= 0) choose(active); else if (options.length === 1) choose(0);
        }
      } else if (e.key === "Escape") {
        if (!listbox.hidden) { e.stopPropagation(); close(); input.value = chosenLabel; }
      }
    });
    input.addEventListener("blur", function () {
      window.setTimeout(function () { close(); input.value = chosenLabel; }, 150);
    });
    if (clearBtn) {
      clearBtn.addEventListener("click", function () { setValue("", ""); input.focus(); });
    }
  });

  function isTyping(el) {
    return el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" ||
      el.tagName === "SELECT" || el.isContentEditable);
  }
  document.addEventListener("keydown", function (e) {
    if (e.ctrlKey || e.metaKey || e.altKey || isTyping(e.target)) return;
    if (e.key === "/") {
      var target = document.querySelector("[data-pg-list-search]") || document.querySelector("[data-pg-nav-filter]");
      if (target) { e.preventDefault(); target.focus(); }
    } else if (e.key === "t") {
      var nav = document.querySelector("[data-pg-nav-filter]");
      if (nav) { e.preventDefault(); nav.focus(); }
    } else if (e.key === "c") {
      var add = document.querySelector("[data-pg-create]");
      if (add) { e.preventDefault(); window.location.href = add.getAttribute("href"); }
    } else if (e.key === "e") {
      var edit = document.querySelector('.pg-detail-actions a[href$="/edit"]');
      if (edit) { e.preventDefault(); window.location.href = edit.getAttribute("href"); }
    }
  });
})();
