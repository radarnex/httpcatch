// ── Theme toggle ───────────────────────────────────────────────────
// The early inline script in layout.html sets data-theme synchronously
// to avoid a flash. This block wires the admin-menu toggle to persist.
(function () {
  var KEY = "httpcatch.theme";

  function setTheme(t) {
    if (t !== "dark" && t !== "light") t = "light";
    document.documentElement.setAttribute("data-theme", t);
    try { localStorage.setItem(KEY, t); } catch (e) {}
    syncThemeButtons(t);
  }

  function syncThemeButtons(t) {
    var l = document.getElementById("theme-light");
    var d = document.getElementById("theme-dark");
    if (l) {
      l.classList.toggle("on", t === "light");
      l.setAttribute("aria-checked", t === "light" ? "true" : "false");
    }
    if (d) {
      d.classList.toggle("on", t === "dark");
      d.setAttribute("aria-checked", t === "dark" ? "true" : "false");
    }
  }

  document.addEventListener("DOMContentLoaded", function () {
    syncThemeButtons(document.documentElement.getAttribute("data-theme") || "light");
    var l = document.getElementById("theme-light");
    var d = document.getElementById("theme-dark");
    if (l) l.addEventListener("click", function () { setTheme("light"); });
    if (d) d.addEventListener("click", function () { setTheme("dark"); });
  });
})();

// ── Admin menu open/close ──────────────────────────────────────────
(function () {
  document.addEventListener("DOMContentLoaded", function () {
    var trigger = document.getElementById("admin-menu-trigger");
    var menu = document.getElementById("admin-menu");
    var mask = document.getElementById("admin-menu-mask");
    if (!trigger || !menu || !mask) return;

    function open() {
      menu.removeAttribute("hidden");
      mask.removeAttribute("hidden");
      trigger.setAttribute("aria-expanded", "true");
    }
    function close() {
      menu.setAttribute("hidden", "");
      mask.setAttribute("hidden", "");
      trigger.setAttribute("aria-expanded", "false");
    }

    trigger.addEventListener("click", function (e) {
      e.stopPropagation();
      if (menu.hasAttribute("hidden")) open(); else close();
    });
    mask.addEventListener("click", close);
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !menu.hasAttribute("hidden")) close();
    });
  });
})();

function shellEscape(s) {
  return "'" + s.replace(/'/g, "'\\''") + "'";
}

function initCurlCopy(root) {
  root = root || document;
  var btn = root.querySelector ? root.querySelector("#curl-copy-btn") : null;
  if (!btn) return;
  btn.removeAttribute("hidden");
  btn.addEventListener("click", function () {
    var method = btn.getAttribute("data-method") || "GET";
    var path = btn.getAttribute("data-path") || "/";
    var headersJSON = btn.getAttribute("data-headers") || "{}";
    var bodyB64 = btn.getAttribute("data-body") || "";

    var headers;
    try { headers = JSON.parse(headersJSON); } catch (_) { headers = {}; }

    var parts = ["curl", "-X", shellEscape(method)];
    Object.keys(headers).forEach(function (name) {
      var values = headers[name];
      if (!Array.isArray(values)) return;
      values.forEach(function (v) { parts.push("-H", shellEscape(name + ": " + v)); });
    });

    var body = "";
    if (bodyB64) {
      try { body = atob(bodyB64); } catch (_) {}
    }
    if (body) parts.push("--data-raw", shellEscape(body));
    parts.push(shellEscape(path));

    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(parts.join(" ")).catch(function (err) {
        console.error("httpcatch: clipboard write failed:", err);
      });
    }
  });
}

// ── Status polling: health pills + buildinfo ───────────────────────
(function () {
  var handle = null;
  var INTERVAL_MS = 5000;
  var lastApplied = {};

  function setHidden(id, hidden) {
    var key = "hidden:" + id;
    if (lastApplied[key] === hidden) return;
    var el = document.getElementById(id);
    if (!el) return;
    if (hidden) el.setAttribute("hidden", "");
    else el.removeAttribute("hidden");
    lastApplied[key] = hidden;
  }

  function setCount(id, n) {
    var key = "count:" + id;
    if (lastApplied[key] === n) return;
    var el = document.getElementById(id);
    if (!el) return;
    var span = el.querySelector("[data-count]");
    if (span) span.textContent = String(n);
    lastApplied[key] = n;
  }

  function setText(selector, text) {
    if (lastApplied[selector] === text) return;
    var el = document.querySelector(selector);
    if (!el) return;
    el.textContent = text;
    lastApplied[selector] = text;
  }

  function refresh() {
    fetch("/status", { credentials: "same-origin", headers: { Accept: "application/json" } })
      .then(function (res) {
        if (!res.ok) {
          console.warn("httpcatch: /status responded with", res.status);
          return null;
        }
        return res.json();
      })
      .then(function (data) {
        if (!data) return;

        setHidden("chip-unredacted", !data.unredacted);

        var dropped = data.counters.dropped_total;
        setHidden("chip-dropped", dropped <= 0);
        setCount("chip-dropped", dropped);

        var redactionErrors = data.counters.redaction_errors_total;
        setHidden("chip-redaction-errors", redactionErrors <= 0);
        setCount("chip-redaction-errors", redactionErrors);

        var serviceCount = data.counters.captured_without_service_total;
        setHidden("chip-service", serviceCount <= 0);
        setCount("chip-service", serviceCount);

        var corrCount = data.counters.captured_without_correlation_total;
        setHidden("chip-correlation", corrCount <= 0);
        setCount("chip-correlation", corrCount);

        setText("#buildinfo", "httpcatch " + data.version + " · built " + data.build_time);
      })
      .catch(function (err) {
        console.error("httpcatch: /status fetch failed:", err);
      });
  }

  function startPolling() {
    if (handle !== null) return;
    handle = setInterval(refresh, INTERVAL_MS);
  }

  function stopPolling() {
    if (handle === null) return;
    clearInterval(handle);
    handle = null;
  }

  document.addEventListener("DOMContentLoaded", function () {
    refresh();
    startPolling();
    initCurlCopy(document);
  });

  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "visible") {
      refresh();
      startPolling();
    } else {
      stopPolling();
    }
  });
})();

// ── Live tail polling — list page only ─────────────────────────────
(function () {
  var POLL_INTERVAL_MS = 2000;
  var MAX_PREPEND = 50;

  function deriveSince(rows) {
    for (var i = 0; i < rows.length; i++) {
      var ts = rows[i].getAttribute && rows[i].getAttribute("data-timestamp");
      if (ts) return ts;
    }
    return "";
  }

  function decideReplaceOrPrepend(newRowCount) {
    if (newRowCount > MAX_PREPEND) return "replace";
    return "prepend";
  }

  function buildPollURL(since) {
    var params = new URLSearchParams(window.location.search);
    params.delete("cursor");
    params.delete("since");
    if (since) params.set("since", since);
    params.set("limit", "50");
    return "/requests?" + params.toString();
  }

  function statusClassCSS(s) {
    if (s == null) return "other";
    if (s >= 200 && s < 300) return "2xx";
    if (s >= 300 && s < 400) return "3xx";
    if (s >= 400 && s < 500) return "4xx";
    if (s >= 500 && s < 600) return "5xx";
    return "other";
  }

  function rowHTML(row) {
    var ts = row.timestamp || "";
    var displayTs = ts ? ts.replace("T", " ").replace(/\.\d+/, "").replace("Z", "") : "";
    var kind = row.kind || "";
    var rowClass = kind !== "request" ? "row-orphan" : "";
    var id = row.id || "";
    var href = "/ui/requests/" + id;

    var badgeHTML;
    if (kind === "request") {
      badgeHTML = '<span class="badge badge-neutral">request</span>';
    } else if (kind === "orphan_response") {
      badgeHTML = '<span class="badge badge-orphan" role="status">orphan_response</span>';
    } else if (kind === "orphan_outbound") {
      badgeHTML = '<span class="badge badge-orphan" role="status">orphan_outbound</span>';
    } else {
      badgeHTML = '<span class="badge badge-neutral">' + escapeHTML(kind) + "</span>";
    }

    var method = row.method || "";
    var methodHTML = method ? '<span class="m-badge m-' + escapeHTML(method) + '">' + escapeHTML(method) + '</span>' : "";

    var statusText = (row.status !== null && row.status !== undefined) ? String(row.status) : "";
    var statusHTML = statusText ? '<span class="s-badge s-' + statusClassCSS(row.status) + '">' + escapeHTML(statusText) + '</span>' : "";

    var ev = row.event_count;
    var eventHTML;
    if (ev === null || ev === undefined) {
      eventHTML = '<span class="event-pill">—</span>';
    } else {
      eventHTML = '<span class="event-pill' + (ev > 0 ? ' has' : '') + '">' + String(ev) + '</span>';
    }

    var svc = escapeHTML(row.service || "");
    var pathS = escapeHTML(row.path || "");

    return (
      '<tr class="' + escapeHTML(rowClass) + ' live-tail-new" data-timestamp="' + escapeHTML(ts) + '" data-id="' + escapeHTML(id) + '">' +
      '<td class="col-time"><a href="' + href + '" class="row-link mono">' + escapeHTML(displayTs) + "</a></td>" +
      '<td class="col-kind">' + badgeHTML + "</td>" +
      '<td class="col-svc"><span class="svc-chip"><span class="dot"></span>' + svc + "</span></td>" +
      '<td class="col-method">' + methodHTML + "</td>" +
      '<td class="col-path">' + pathS + "</td>" +
      '<td class="col-status">' + statusHTML + "</td>" +
      '<td class="col-events">' + eventHTML + "</td>" +
      "</tr>"
    );
  }

  function escapeHTML(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function mergePrepend(tbody, newRowsHTML) {
    var empty = tbody.querySelector(".empty-state");
    if (empty && empty.parentNode) {
      empty.parentNode.removeChild(empty);
    }
    tbody.insertAdjacentHTML("afterbegin", newRowsHTML);
  }

  window.__livetail = {
    deriveSince: deriveSince,
    decideReplaceOrPrepend: decideReplaceOrPrepend,
    buildPollURL: buildPollURL,
    rowHTML: rowHTML,
    mergePrepend: mergePrepend,
  };

  document.addEventListener("DOMContentLoaded", function () {
    var toggle = document.getElementById("live-tail-toggle");
    if (!toggle) return;

    var checkbox = document.getElementById("live-tail-checkbox");
    var statusDiv = document.getElementById("live-tail-status");
    var tbody = document.getElementById("requests-tbody");
    var paginationNav = document.getElementById("pagination-nav");

    var intervalHandle = null;
    var failureCount = 0;
    var active = false;

    function syncCheckbox() { checkbox.checked = active; }

    function readHash() { return window.location.hash.indexOf("live=1") !== -1; }

    function writeHash(on) {
      if (on) {
        if (window.location.hash.indexOf("live=1") === -1) {
          window.location.hash = "live=1";
        }
      } else {
        var h = window.location.hash.replace(/[#&]?live=1/, "").replace(/^#$/, "");
        history.replaceState(null, "", window.location.pathname + window.location.search + (h ? "#" + h : ""));
      }
    }

    function setPaginationDisabled(disabled) {
      if (!paginationNav) return;
      var links = paginationNav.querySelectorAll("a.page-link");
      for (var i = 0; i < links.length; i++) {
        if (disabled) {
          links[i].setAttribute("aria-disabled", "true");
          links[i].classList.add("page-link-disabled");
        } else {
          links[i].removeAttribute("aria-disabled");
          links[i].classList.remove("page-link-disabled");
        }
      }
    }

    if (paginationNav) {
      paginationNav.addEventListener("click", function (e) {
        var link = e.target.closest("a.page-link");
        if (!link) return;
        if (link.getAttribute("aria-disabled") === "true") {
          e.preventDefault();
        }
      });
    }

    function showStatus(msg) {
      if (!statusDiv) return;
      statusDiv.textContent = msg;
      statusDiv.removeAttribute("hidden");
    }

    function clearStatus() {
      if (!statusDiv) return;
      statusDiv.setAttribute("hidden", "");
      statusDiv.textContent = "";
    }

    function poll() {
      if (!tbody) return;
      var rows = tbody.querySelectorAll("tr[data-timestamp]");
      var since = deriveSince(rows);
      var url = buildPollURL(since);

      fetch(url, { credentials: "same-origin", headers: { Accept: "application/json" } })
        .then(function (res) {
          if (!res.ok) throw new Error("HTTP " + res.status);
          return res.json();
        })
        .then(function (data) {
          failureCount = 0;
          clearStatus();

          var records = data.records || [];
          if (records.length === 0) return;

          var seen = {};
          var existing = tbody.querySelectorAll("tr[data-id]");
          for (var k = 0; k < existing.length; k++) {
            var id = existing[k].getAttribute("data-id");
            if (id) seen[id] = true;
          }
          var fresh = [];
          for (var j = 0; j < records.length; j++) {
            var rid = records[j].id;
            if (rid && seen[rid]) continue;
            fresh.push(records[j]);
          }
          if (fresh.length === 0) return;

          var decision = decideReplaceOrPrepend(fresh.length);
          var html = "";
          for (var i = 0; i < fresh.length; i++) html += rowHTML(fresh[i]);
          if (decision === "replace") tbody.innerHTML = html;
          else mergePrepend(tbody, html);
          document.dispatchEvent(new Event("httpcatch:rows-updated"));
        })
        .catch(function (err) {
          console.error("httpcatch: live-tail poll failed:", err);
          failureCount++;
          if (failureCount >= 3) {
            stop();
            showStatus("Live tail paused — connection error. Click to retry.");
          }
        });
    }

    function start() {
      if (intervalHandle !== null) return;
      intervalHandle = setInterval(poll, POLL_INTERVAL_MS);
    }

    function stop() {
      if (intervalHandle === null) return;
      clearInterval(intervalHandle);
      intervalHandle = null;
    }

    function activate() {
      active = true;
      syncCheckbox();
      writeHash(true);
      setPaginationDisabled(true);
      clearStatus();
      failureCount = 0;
      poll();
      start();
    }

    function deactivate() {
      active = false;
      syncCheckbox();
      writeHash(false);
      stop();
      setPaginationDisabled(false);
      clearStatus();
    }

    if (statusDiv) {
      statusDiv.addEventListener("click", function () {
        if (!active) {
          failureCount = 0;
          clearStatus();
          activate();
        }
      });
    }

    checkbox.addEventListener("change", function () {
      if (checkbox.checked) activate();
      else deactivate();
    });

    document.addEventListener("visibilitychange", function () {
      if (!active) return;
      if (document.visibilityState === "visible") {
        start();
        poll();
      } else {
        stop();
      }
    });

    if (readHash()) activate();
  });
})();

// ── Search, time picker, side panel, histogram, saved views, export ──
(function () {
  // ── Shorthand parsing ────────────────────────────────────────────
  function parseShorthand(s) {
    if (!s) return 0;
    var m = String(s).trim().toLowerCase().match(/^(\d+)\s*(s|m|h|d|w|mo)$/);
    if (!m) return 0;
    var n = parseInt(m[1], 10);
    switch (m[2]) {
      case "s":  return n * 1000;
      case "m":  return n * 60 * 1000;
      case "h":  return n * 60 * 60 * 1000;
      case "d":  return n * 24 * 60 * 60 * 1000;
      case "w":  return n * 7 * 24 * 60 * 60 * 1000;
      case "mo": return n * 30 * 24 * 60 * 60 * 1000;
    }
    return 0;
  }

  function rfc3339(d) {
    return d.toISOString().replace(/\.\d{3}Z$/, "Z");
  }

  function toDatetimeLocal(d) {
    var pad = function (n) { return n < 10 ? "0" + n : "" + n; };
    return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) +
      "T" + pad(d.getHours()) + ":" + pad(d.getMinutes());
  }

  function applyPreset(preset) {
    var sinceEl = document.getElementById("f-since");
    var untilEl = document.getElementById("f-until");
    if (!sinceEl || !untilEl) return;

    if (preset === "live") {
      activateLiveTail();
      return;
    }
    var ms = parseShorthand(preset);
    if (ms <= 0) return;
    var until = new Date();
    var since = new Date(until.getTime() - ms);
    sinceEl.value = rfc3339(since);
    untilEl.value = rfc3339(until);
    submitForm();
  }

  function applyAbsolute(fromStr, toStr) {
    var sinceEl = document.getElementById("f-since");
    var untilEl = document.getElementById("f-until");
    if (!sinceEl || !untilEl) return;
    var from = fromStr ? new Date(fromStr) : null;
    var to = toStr ? new Date(toStr) : null;
    if (!from || isNaN(from.getTime())) return;
    if (!to || isNaN(to.getTime())) to = new Date();
    sinceEl.value = rfc3339(from);
    untilEl.value = rfc3339(to);
    submitForm();
  }

  function activateLiveTail() {
    var cb = document.getElementById("live-tail-checkbox");
    if (!cb) return;
    if (!cb.checked) {
      cb.checked = true;
      cb.dispatchEvent(new Event("change"));
    }
    var trigger = document.getElementById("picker-trigger");
    if (trigger) trigger.classList.add("is-live");
    var input = document.getElementById("picker-input");
    if (input) input.value = "Live tail";
    var pop = document.getElementById("picker-popover");
    if (pop) {
      pop.setAttribute("hidden", "");
      if (trigger) trigger.setAttribute("aria-expanded", "false");
    }
  }

  function submitForm() {
    var form = document.getElementById("filter-form");
    if (!form) return;
    form.submit();
  }

  function prettyPresetLabel(p) {
    var map = {
      "15m": "Past 15 minutes",
      "1h":  "Past 1 hour",
      "4h":  "Past 4 hours",
      "1d":  "Past 1 day",
      "2d":  "Past 2 days",
      "3d":  "Past 3 days",
      "1w":  "Past 7 days",
      "15d": "Past 15 days",
      "1mo": "Past 1 month",
    };
    return map[p] || p;
  }

  var TIME_FMT = new Intl.DateTimeFormat(undefined, {
    month: "short", day: "numeric", hour: "numeric", minute: "2-digit",
  });

  function formatRange(from, to) {
    return TIME_FMT.format(from) + " – " + TIME_FMT.format(to);
  }

  function wirePicker() {
    var trigger = document.getElementById("picker-trigger");
    var input = document.getElementById("picker-input");
    var caret = document.getElementById("picker-caret");
    var popover = document.getElementById("picker-popover");
    if (!trigger || !popover || !input) return;

    var viewPresets = document.getElementById("picker-view-presets");
    var viewCal = document.getElementById("picker-view-calendar");

    function openPopover() {
      popover.removeAttribute("hidden");
      trigger.setAttribute("aria-expanded", "true");
      showPresets();
    }
    function closePopover() {
      popover.setAttribute("hidden", "");
      trigger.setAttribute("aria-expanded", "false");
    }
    function showPresets() {
      viewPresets.hidden = false;
      viewCal.hidden = true;
    }
    function showCalendar() {
      viewPresets.hidden = true;
      viewCal.hidden = false;
      var since = document.getElementById("f-since");
      var until = document.getElementById("f-until");
      var fromEl = document.getElementById("picker-from");
      var toEl = document.getElementById("picker-to");
      if (since && since.value && fromEl) {
        var d = new Date(since.value);
        if (!isNaN(d.getTime())) fromEl.value = toDatetimeLocal(d);
      }
      if (until && until.value && toEl) {
        var d2 = new Date(until.value);
        if (!isNaN(d2.getTime())) toEl.value = toDatetimeLocal(d2);
      }
    }

    input.addEventListener("focus", function () {
      if (popover.hasAttribute("hidden")) openPopover();
      input.select();
    });

    caret.addEventListener("click", function (e) {
      e.stopPropagation();
      if (popover.hasAttribute("hidden")) openPopover();
      else closePopover();
    });

    input.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        e.preventDefault();
        var raw = input.value.trim();
        var ms = parseShorthand(raw);
        if (ms > 0) applyPreset(raw);
        return;
      }
      if (e.key === "Escape") {
        closePopover();
        input.blur();
      }
    });

    viewPresets.addEventListener("click", function (e) {
      var btn = e.target.closest("button[data-preset]");
      if (!btn) return;
      e.stopPropagation();
      applyPreset(btn.getAttribute("data-preset"));
    });

    var calLink = document.getElementById("picker-cal-link");
    if (calLink) {
      calLink.addEventListener("click", function (e) {
        e.stopPropagation();
        showCalendar();
      });
    }

    var back = document.getElementById("picker-back");
    if (back) {
      back.addEventListener("click", function (e) {
        e.stopPropagation();
        showPresets();
      });
    }

    var apply = document.getElementById("picker-apply");
    var fromEl = document.getElementById("picker-from");
    var toEl = document.getElementById("picker-to");
    if (apply && fromEl && toEl) {
      apply.addEventListener("click", function (e) {
        e.stopPropagation();
        applyAbsolute(fromEl.value, toEl.value);
      });
    }

    document.addEventListener("click", function (e) {
      if (popover.hasAttribute("hidden")) return;
      if (popover.contains(e.target) || trigger.contains(e.target)) return;
      closePopover();
    });
  }

  function refreshTriggerLabel() {
    var input = document.getElementById("picker-input");
    var trigger = document.getElementById("picker-trigger");
    if (!input) return;

    var cb = document.getElementById("live-tail-checkbox");
    if (cb && cb.checked) {
      input.value = "Live tail";
      if (trigger) trigger.classList.add("is-live");
      return;
    }
    if (trigger) trigger.classList.remove("is-live");

    var since = document.getElementById("f-since");
    var until = document.getElementById("f-until");
    if (since && until && since.value && until.value) {
      var s = new Date(since.value);
      var u = new Date(until.value);
      if (!isNaN(s.getTime()) && !isNaN(u.getTime())) {
        var match = matchPresetMs(u.getTime() - s.getTime());
        input.value = match ? prettyPresetLabel(match) : formatRange(s, u);
        return;
      }
    }
    input.value = "";
  }

  function matchPresetMs(ms) {
    var presets = {
      "15m": 15 * 60 * 1000,
      "1h":  60 * 60 * 1000,
      "4h":  4 * 60 * 60 * 1000,
      "1d":  24 * 60 * 60 * 1000,
      "2d":  2 * 24 * 60 * 60 * 1000,
      "3d":  3 * 24 * 60 * 60 * 1000,
      "1w":  7 * 24 * 60 * 60 * 1000,
      "15d": 15 * 24 * 60 * 60 * 1000,
      "1mo": 30 * 24 * 60 * 60 * 1000,
    };
    var toleranceMs = 60 * 1000;
    for (var key in presets) {
      if (Math.abs(presets[key] - ms) < toleranceMs) return key;
    }
    return null;
  }

  // ── Side-panel detail (drawer) ───────────────────────────────────
  function cssEscape(s) {
    if (window.CSS && CSS.escape) return CSS.escape(s);
    return String(s).replace(/[^a-zA-Z0-9_-]/g, "\\$&");
  }

  function wireDrawerTabs(root) {
    var tabs = root.querySelectorAll(".drawer-tab[data-tab]");
    var sections = root.querySelectorAll(".tab-section[data-tab]");
    if (!tabs.length) return;
    tabs.forEach(function (t) {
      t.addEventListener("click", function () {
        var key = t.getAttribute("data-tab");
        tabs.forEach(function (x) {
          var on = x.getAttribute("data-tab") === key;
          x.classList.toggle("on", on);
          x.setAttribute("aria-selected", on ? "true" : "false");
        });
        sections.forEach(function (s) {
          var on = s.getAttribute("data-tab") === key;
          s.classList.toggle("on", on);
          if (on) s.removeAttribute("hidden");
          else s.setAttribute("hidden", "");
        });
      });
    });
  }

  function wireSidePanel() {
    var panel = document.getElementById("detail-panel");
    var scrim = document.getElementById("detail-panel-scrim");
    var closeBtn = document.getElementById("detail-panel-close");
    var expandLink = document.getElementById("detail-panel-expand");
    var pathLabel = document.getElementById("detail-panel-path");
    var body = document.getElementById("detail-panel-body");
    var tbody = document.getElementById("requests-tbody");
    if (!panel || !tbody) return;

    var currentID = null;

    function openWith(id, url) {
      currentID = id;
      panel.removeAttribute("hidden");
      scrim.removeAttribute("hidden");
      if (expandLink) expandLink.setAttribute("href", url);
      if (pathLabel) pathLabel.textContent = "";
      body.innerHTML = '<div class="drawer-loading">Loading…</div>';

      tbody.querySelectorAll("tr.row-active").forEach(function (r) {
        r.classList.remove("row-active");
      });
      var row = tbody.querySelector('tr[data-id="' + cssEscape(id) + '"]');
      if (row) row.classList.add("row-active");

      fetch(url, { credentials: "same-origin", headers: { Accept: "text/html" } })
        .then(function (res) {
          if (!res.ok) throw new Error("HTTP " + res.status);
          return res.text();
        })
        .then(function (html) {
          if (currentID !== id) return;
          var doc = new DOMParser().parseFromString(html, "text/html");
          renderDrawerFromDoc(doc, body, pathLabel);
          initCurlCopy(body);
          wireDrawerTabs(body);
        })
        .catch(function (err) {
          if (currentID !== id) return;
          body.innerHTML = '<div class="inline-error">Failed to load: ' + escapeHTMLLocal(err && err.message) + "</div>";
        });
    }

    function close() {
      panel.setAttribute("hidden", "");
      scrim.setAttribute("hidden", "");
      currentID = null;
      tbody.querySelectorAll("tr.row-active").forEach(function (r) {
        r.classList.remove("row-active");
      });
    }

    closeBtn.addEventListener("click", close);
    scrim.addEventListener("click", close);
    document.addEventListener("keydown", function (e) {
      if (e.key !== "Escape") return;
      if (panel.hasAttribute("hidden")) return;
      var t = e.target;
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return;
      close();
    });

    tbody.addEventListener("click", function (e) {
      if (e.button !== 0) return;
      var modified = e.metaKey || e.ctrlKey || e.shiftKey;

      var directLink = e.target.closest("a.row-link");
      if (directLink) {
        if (modified) return;
        var trLink = directLink.closest("tr[data-id]");
        if (!trLink) return;
        e.preventDefault();
        openWith(trLink.getAttribute("data-id"), directLink.getAttribute("href"));
        return;
      }

      if (e.target.closest("a, button, input, label, select, textarea")) return;

      var sel = window.getSelection && window.getSelection();
      if (sel && sel.type === "Range" && !sel.isCollapsed) return;

      var row = e.target.closest("tr[data-id]");
      if (!row) return;
      var link = row.querySelector("a.row-link");
      if (!link) return;
      var url = link.getAttribute("href");
      var id = row.getAttribute("data-id");
      if (modified) {
        window.open(url, "_blank", "noopener");
        return;
      }
      openWith(id, url);
    });
  }

  function renderDrawerFromDoc(doc, body, pathLabel) {
    body.innerHTML = "";

    var pageHead = doc.querySelector(".page-head");
    if (pathLabel && pageHead) {
      var path = pageHead.querySelector(".mono");
      if (path) pathLabel.textContent = path.textContent;
    }

    var curl = doc.querySelector("#curl-copy-btn");
    if (curl) body.appendChild(document.adoptNode(curl));

    var tabs = doc.querySelector(".drawer-tabs");
    if (tabs) body.appendChild(document.adoptNode(tabs));

    doc.querySelectorAll(".tab-section").forEach(function (s) {
      body.appendChild(document.adoptNode(s));
    });

    // Fallback for pages without tabs (e.g. events_detail.html): copy the
    // meta-grid + everything that lives in the page's drawer-body container.
    if (!tabs) {
      var dbody = doc.querySelector(".drawer-body");
      if (dbody) {
        Array.prototype.slice.call(dbody.children).forEach(function (c) {
          body.appendChild(document.adoptNode(c));
        });
      }
    }
  }

  function escapeHTMLLocal(s) {
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }

  // ── Histogram (stacked by status class) ──────────────────────────
  function statusBucket(s) {
    if (s == null) return null;
    if (s >= 200 && s < 300) return "2";
    if (s >= 300 && s < 400) return "3";
    if (s >= 400 && s < 500) return "4";
    if (s >= 500 && s < 600) return "5";
    return null;
  }

  function readVar(name, fallback) {
    var v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    return v || fallback;
  }

  function renderHistogramAndCount() {
    var canvas = document.getElementById("histogram-canvas");
    var tbody = document.getElementById("requests-tbody");
    var resultsCount = document.getElementById("results-count");
    var resultsWindow = document.getElementById("results-window");
    if (!canvas || !tbody) return;

    var rows = tbody.querySelectorAll("tr[data-timestamp]");
    var count = rows.length;
    if (resultsCount) resultsCount.textContent = String(count);

    var since = document.getElementById("f-since");
    var until = document.getElementById("f-until");
    if (resultsWindow) {
      if (since && until && since.value && until.value) {
        var s = new Date(since.value); var u = new Date(until.value);
        if (!isNaN(s.getTime()) && !isNaN(u.getTime())) {
          resultsWindow.textContent = formatRange(s, u);
        } else { resultsWindow.textContent = ""; }
      } else { resultsWindow.textContent = ""; }
    }

    if (count === 0) {
      var c = canvas.getContext("2d");
      c.clearRect(0, 0, canvas.width, canvas.height);
      return;
    }

    var timestamps = [];
    var buckets2 = []; // parallel: status class char
    rows.forEach(function (r) {
      var ts = r.getAttribute("data-timestamp");
      var d = new Date(ts);
      if (isNaN(d.getTime())) return;
      timestamps.push(d.getTime());
      var statusEl = r.querySelector(".s-badge");
      var cls = null;
      if (statusEl) {
        var m = statusEl.className.match(/s-(\d)xx/);
        if (m) cls = m[1];
      }
      buckets2.push(cls);
    });
    if (!timestamps.length) return;

    var minT = Math.min.apply(null, timestamps);
    var maxT = Math.max.apply(null, timestamps);
    if (since && since.value) {
      var sd = new Date(since.value); if (!isNaN(sd.getTime())) minT = sd.getTime();
    }
    if (until && until.value) {
      var ud = new Date(until.value); if (!isNaN(ud.getTime())) maxT = ud.getTime();
    }
    if (maxT <= minT) maxT = minT + 60000;

    var bn = 40;
    var bw = (maxT - minT) / bn;
    var stacks = new Array(bn);
    for (var i = 0; i < bn; i++) stacks[i] = { "2": 0, "3": 0, "4": 0, "5": 0, other: 0 };
    timestamps.forEach(function (t, k) {
      var idx = Math.floor((t - minT) / bw);
      if (idx < 0) idx = 0;
      if (idx >= bn) idx = bn - 1;
      var cls = buckets2[k];
      stacks[idx][cls || "other"]++;
    });

    drawHistogram(canvas, stacks, minT, maxT);
  }

  var AXIS_HEIGHT = 18;

  function drawHistogram(canvas, stacks, minT, maxT) {
    var dpr = window.devicePixelRatio || 1;
    var w = canvas.clientWidth;
    var h = canvas.clientHeight;
    canvas.width = Math.floor(w * dpr);
    canvas.height = Math.floor(h * dpr);
    var ctx = canvas.getContext("2d");
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, w, h);

    var chartH = h - AXIS_HEIGHT;

    var max = 0;
    stacks.forEach(function (b) {
      var total = b["2"] + b["3"] + b["4"] + b["5"] + b.other;
      if (total > max) max = total;
    });

    if (max > 0) {
      var n = stacks.length;
      var barW = w / n;
      var gap = Math.max(1, barW * 0.15);
      var colors = {
        "2": readVar("--s-2xx", "#0d9968"),
        "3": readVar("--s-3xx", "#0284c7"),
        "4": readVar("--s-4xx", "#b8770a"),
        "5": readVar("--s-5xx", "#e11d48"),
        "other": readVar("--text-4", "#a3aab8"),
      };
      stacks.forEach(function (b, i) {
        var total = b["2"] + b["3"] + b["4"] + b["5"] + b.other;
        if (total === 0) return;
        var x = i * barW + gap / 2;
        var y = chartH;
        var stack = ["2", "3", "4", "5", "other"];
        for (var s = 0; s < stack.length; s++) {
          var cls = stack[s];
          var v = b[cls];
          if (v === 0) continue;
          var hp = (v / max) * (chartH - 4);
          y -= hp;
          ctx.fillStyle = colors[cls];
          ctx.fillRect(x, y, Math.max(1, barW - gap), hp);
        }
      });
    }

    ctx.strokeStyle = readVar("--border-strong", "#e4e4e7");
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(0, chartH + 0.5);
    ctx.lineTo(w, chartH + 0.5);
    ctx.stroke();

    drawAxisLabels(ctx, w, h, chartH, minT, maxT);
  }

  function drawAxisLabels(ctx, w, h, chartH, minT, maxT) {
    if (!minT || !maxT || maxT <= minT) return;
    var ticks = 6;
    var spanMs = maxT - minT;
    var fmt = pickAxisFormat(spanMs);
    ctx.fillStyle = readVar("--text-3", "#71717a");
    ctx.font = "10px ui-monospace, SFMono-Regular, Menlo, monospace";
    ctx.textBaseline = "bottom";

    for (var i = 0; i <= ticks; i++) {
      var t = minT + (spanMs * i / ticks);
      var x = (w * i / ticks);
      var label = fmt(new Date(t));
      var metrics = ctx.measureText(label);
      var tx = x - metrics.width / 2;
      if (i === 0) tx = 1;
      if (i === ticks) tx = w - metrics.width - 1;
      ctx.fillText(label, tx, h - 2);
    }
  }

  function pad2(n) { return n < 10 ? "0" + n : "" + n; }
  var MONTH_ABBR = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
  function pickAxisFormat(spanMs) {
    var hourMs = 3600 * 1000;
    var dayMs = 24 * hourMs;
    if (spanMs <= 4 * hourMs) return function (d) { return pad2(d.getHours()) + ":" + pad2(d.getMinutes()); };
    if (spanMs <= 2 * dayMs) return function (d) { return pad2(d.getHours()) + ":00"; };
    return function (d) { return MONTH_ABBR[d.getMonth()] + " " + d.getDate(); };
  }

  // ── Kibana-style search box ──────────────────────────────────────
  var SEARCH_KEYS = ["service", "method", "status", "path", "body", "correlation_id", "source_ip"];

  function parseSearch(s) {
    var out = {};
    SEARCH_KEYS.forEach(function (k) { out[k] = ""; });
    if (!s) return out;
    var freeText = [];
    var tokens = s.match(/(?:[^\s"]+|"[^"]*")+/g) || [];
    tokens.forEach(function (tok) {
      var idx = tok.indexOf(":");
      if (idx <= 0) { freeText.push(strip(tok)); return; }
      var key = tok.slice(0, idx).toLowerCase();
      var val = strip(tok.slice(idx + 1));
      if (SEARCH_KEYS.indexOf(key) !== -1) out[key] = val;
      else freeText.push(tok);
    });
    if (freeText.length && !out.body) out.body = freeText.join(" ").trim();
    return out;
  }

  function strip(tok) {
    if (tok.length >= 2 && tok.charAt(0) === '"' && tok.charAt(tok.length - 1) === '"') {
      return tok.slice(1, -1);
    }
    return tok;
  }

  function wireSearch() {
    var input = document.getElementById("search-input");
    var clear = document.getElementById("search-clear");
    var form = document.getElementById("filter-form");
    if (!input || !form) return;

    // Active filters round-trip via the visible chips above the input.
    // Leaving the bare input empty avoids duplicating each chip as text.
    if (clear) clear.hidden = true;

    input.addEventListener("input", function () {
      if (clear) clear.hidden = input.value === "";
    });

    if (clear) {
      clear.addEventListener("click", function () {
        input.value = "";
        clear.hidden = true;
        input.focus();
      });
    }

    input.addEventListener("keydown", function (e) {
      if (e.key !== "Enter") return;
      e.preventDefault();
      flushSearchToMirrors();
      submitForm();
    });

    form.addEventListener("submit", function () {
      flushSearchToMirrors();
    });

    // Chip remove buttons clear the corresponding hidden input and submit.
    var host = document.getElementById("query-input-host");
    if (host) {
      host.addEventListener("click", function (e) {
        var x = e.target.closest("[data-chip-remove]");
        if (!x) return;
        e.preventDefault();
        var key = x.getAttribute("data-chip-remove");
        var map = {
          service: "f-service", method: "f-method", status: "f-status",
          path: "f-path", body: "f-body",
        };
        var fid = map[key];
        if (fid) setVal(fid, "");
        submitForm();
      });
    }

    function flushSearchToMirrors() {
      var parsed = parseSearch(input.value);
      // Existing chips already round-trip via hidden inputs; only merge new
      // key:value tokens the user typed into the bare input.
      if (parsed.service) setVal("f-service", parsed.service);
      if (parsed.method) setVal("f-method", parsed.method);
      if (parsed.status) setVal("f-status", parsed.status);
      if (parsed.path) setVal("f-path", parsed.path);
      if (parsed.body) setVal("f-body", parsed.body);
      if (parsed.correlation_id) setVal("f-correlation-id", parsed.correlation_id);
      if (parsed.source_ip) setVal("f-source-ip", parsed.source_ip);
    }
  }

  function setVal(id, v) { var el = document.getElementById(id); if (el) el.value = v; }

  // ── Saved views (localStorage) ───────────────────────────────────
  var SAVED_KEY = "httpcatch.savedViews";

  function loadSaved() {
    try {
      var raw = localStorage.getItem(SAVED_KEY);
      if (!raw) return [];
      var arr = JSON.parse(raw);
      return Array.isArray(arr) ? arr : [];
    } catch (e) { return []; }
  }
  function storeSaved(arr) {
    try { localStorage.setItem(SAVED_KEY, JSON.stringify(arr)); } catch (e) {}
  }

  function currentQueryString() {
    var form = document.getElementById("filter-form");
    if (!form) return "";
    var fd = new FormData(form);
    var parts = [];
    fd.forEach(function (v, k) {
      if (!v) return;
      parts.push(encodeURIComponent(k) + "=" + encodeURIComponent(v));
    });
    return parts.join("&");
  }

  function renderSavedList() {
    var list = document.getElementById("saved-views-list");
    var empty = document.getElementById("saved-views-empty");
    if (!list) return;
    var views = loadSaved();
    list.innerHTML = "";
    if (!views.length) {
      if (empty) empty.removeAttribute("hidden");
      return;
    }
    if (empty) empty.setAttribute("hidden", "");
    views.forEach(function (v, i) {
      var item = document.createElement("div");
      item.className = "saved-item";
      var a = document.createElement("a");
      a.className = "saved-item-name";
      a.href = "/ui/requests" + (v.query ? "?" + v.query : "");
      a.textContent = v.name;
      var q = document.createElement("span");
      q.className = "saved-item-q";
      q.textContent = v.query;
      var del = document.createElement("button");
      del.type = "button";
      del.className = "saved-item-del";
      del.setAttribute("aria-label", "Delete");
      del.textContent = "×";
      del.addEventListener("click", function (e) {
        e.preventDefault();
        var arr = loadSaved();
        arr.splice(i, 1);
        storeSaved(arr);
        renderSavedList();
      });
      item.appendChild(a);
      item.appendChild(q);
      item.appendChild(del);
      list.appendChild(item);
    });
  }

  function wireSavedViews() {
    var trigger = document.getElementById("saved-views-trigger");
    var pop = document.getElementById("saved-views-popover");
    var form = document.getElementById("saved-views-form");
    var nameInput = document.getElementById("saved-views-name");
    if (!trigger || !pop) return;

    trigger.addEventListener("click", function (e) {
      e.stopPropagation();
      if (pop.hasAttribute("hidden")) {
        renderSavedList();
        pop.removeAttribute("hidden");
        trigger.setAttribute("aria-expanded", "true");
      } else {
        pop.setAttribute("hidden", "");
        trigger.setAttribute("aria-expanded", "false");
      }
    });

    document.addEventListener("click", function (e) {
      if (pop.hasAttribute("hidden")) return;
      if (pop.contains(e.target) || trigger.contains(e.target)) return;
      pop.setAttribute("hidden", "");
      trigger.setAttribute("aria-expanded", "false");
    });

    if (form && nameInput) {
      form.addEventListener("submit", function (e) {
        e.preventDefault();
        var name = nameInput.value.trim();
        if (!name) return;
        var arr = loadSaved();
        arr.push({ name: name, query: currentQueryString() });
        storeSaved(arr);
        nameInput.value = "";
        renderSavedList();
      });
    }
  }

  // ── JSON export ──────────────────────────────────────────────────
  function wireExport() {
    var btn = document.getElementById("export-json-btn");
    if (!btn) return;
    var originalHTML = btn.innerHTML;
    btn.addEventListener("click", function () {
      btn.disabled = true;
      btn.textContent = "Exporting…";

      collectAllRecords()
        .then(function (records) {
          var blob = new Blob([JSON.stringify(records, null, 2)], { type: "application/json" });
          var a = document.createElement("a");
          a.href = URL.createObjectURL(blob);
          a.download = "httpcatch-" + new Date().toISOString().replace(/[:.]/g, "-") + ".json";
          document.body.appendChild(a);
          a.click();
          URL.revokeObjectURL(a.href);
          a.remove();
        })
        .catch(function (err) {
          console.error("httpcatch: export failed:", err);
          alert("Export failed: " + (err && err.message));
        })
        .then(function () {
          btn.disabled = false;
          btn.innerHTML = originalHTML;
        });
    });
  }

  function collectAllRecords() {
    var params = new URLSearchParams(window.location.search);
    params.delete("cursor");
    params.set("limit", "200");
    var all = [];
    var MAX_PAGES = 50;

    function fetchPage(cursor, page) {
      if (page >= MAX_PAGES) return Promise.resolve();
      var p = new URLSearchParams(params);
      if (cursor) p.set("cursor", cursor);
      return fetch("/requests?" + p.toString(), {
        credentials: "same-origin",
        headers: { Accept: "application/json" },
      }).then(function (res) {
        if (!res.ok) throw new Error("HTTP " + res.status);
        return res.json();
      }).then(function (data) {
        var records = data.records || [];
        for (var i = 0; i < records.length; i++) all.push(records[i]);
        if (data.next_cursor) return fetchPage(data.next_cursor, page + 1);
      });
    }
    return fetchPage("", 0).then(function () { return all; });
  }

  // ── Bootstrapping ────────────────────────────────────────────────
  document.addEventListener("DOMContentLoaded", function () {
    refreshTriggerLabel();
    wirePicker();
    wireSearch();
    wireSidePanel();
    wireSavedViews();
    wireExport();
    renderHistogramAndCount();

    // Detail-page tabs (full-page detail view).
    wireDrawerTabs(document);

    var rafHandle = 0;
    window.addEventListener("resize", function () {
      if (rafHandle) return;
      rafHandle = requestAnimationFrame(function () {
        rafHandle = 0;
        renderHistogramAndCount();
      });
    });

    document.addEventListener("httpcatch:rows-updated", renderHistogramAndCount);
  });
})();
