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
    form.addEventListener("submit", function () { dirty = false; });
    window.addEventListener("beforeunload", function (e) {
      if (dirty) { e.preventDefault(); e.returnValue = ""; }
    });
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

  document.querySelectorAll(".pg-fk[data-pg-fk-url]").forEach(function (wrap) {
    var input = wrap.querySelector(".pg-fk-input");
    var url = wrap.getAttribute("data-pg-fk-url");
    if (!input || !url) return;

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
      input.value = options[i].value;
      var current = wrap.querySelector(".pg-fk-current");
      if (!current) {
        current = document.createElement("span");
        current.className = "pg-fk-current";
        wrap.appendChild(current);
      }
      current.textContent = options[i].label;
      close();
    }

    function render(resp) {
      options = (resp && resp.options) || [];
      listbox.textContent = "";
      if (!options.length) {
        var none = document.createElement("li");
        none.className = "pg-fk-note";
        none.textContent = "No matches.";
        listbox.appendChild(none);
      }
      options.forEach(function (opt, i) {
        var li = document.createElement("li");
        li.className = "pg-fk-option";
        li.id = listbox.id + "-" + i;
        li.setAttribute("role", "option");
        li.setAttribute("aria-selected", "false");
        var label = document.createElement("span");
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
        var plain = document.createElement("li");
        plain.className = "pg-fk-note";
        plain.textContent = "This resource cannot be searched -- showing the first rows.";
        listbox.appendChild(plain);
      } else if (resp && resp.truncated) {
        var more = document.createElement("li");
        more.className = "pg-fk-note";
        more.textContent = "More matches exist -- keep typing to narrow.";
        listbox.appendChild(more);
      }
      listbox.hidden = false;
      input.setAttribute("aria-expanded", "true");
      active = -1;
    }

    function search() {
      var mine = ++seq;
      fetch(url + "?q=" + encodeURIComponent(input.value), {
        credentials: "same-origin",
        headers: { "Accept": "application/json" }
      }).then(function (res) {
        if (!res.ok) return null;
        return res.json();
      }).then(function (data) {
        if (mine !== seq) return;
        if (data) render(data); else close();
      }).catch(function () { close(); });
    }

    input.addEventListener("input", function () {
      window.clearTimeout(timer);
      timer = window.setTimeout(search, 180);
    });
    input.addEventListener("keydown", function (e) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        if (listbox.hidden) search(); else highlight(active + 1);
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        if (!listbox.hidden) highlight(active - 1);
      } else if (e.key === "Enter") {
        if (!listbox.hidden && active >= 0) { e.preventDefault(); choose(active); }
      } else if (e.key === "Escape") {
        if (!listbox.hidden) { e.stopPropagation(); close(); }
      }
    });
    input.addEventListener("blur", function () { window.setTimeout(close, 120); });
  });

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
