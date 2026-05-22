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

// ── Timezone preference ────────────────────────────────────────────
// Captured records are stored in UTC; the UI defaults to displaying them in
// UTC so the rendered time matches the underlying record. The operator can
// switch to browser-local time from the admin menu.
window.__tz = (function () {
  var KEY = "httpcatch.tz";

  function read() {
    try {
      var v = localStorage.getItem(KEY);
      return v === "local" ? "local" : "UTC";
    } catch (e) { return "UTC"; }
  }

  var current = read();
  var listeners = [];
  // Intl.DateTimeFormat construction is one of the heavier Web APIs (locale
  // data lookup + format-pattern compile), and the histogram axis + the
  // per-row reformat walk both call into formatter/parts in tight loops. Cache
  // one formatter per opts shape; bust on tz change.
  var fmtCache = {};

  function set(t) {
    current = t === "local" ? "local" : "UTC";
    try { localStorage.setItem(KEY, current); } catch (e) {}
    fmtCache = {};
    syncButtons(current);
    for (var i = 0; i < listeners.length; i++) {
      try { listeners[i](current); } catch (_) {}
    }
  }

  function get() { return current; }

  function onChange(fn) { listeners.push(fn); }

  function syncButtons(t) {
    var u = document.getElementById("tz-utc");
    var l = document.getElementById("tz-local");
    if (u) {
      u.classList.toggle("on", t === "UTC");
      u.setAttribute("aria-checked", t === "UTC" ? "true" : "false");
    }
    if (l) {
      l.classList.toggle("on", t === "local");
      l.setAttribute("aria-checked", t === "local" ? "true" : "false");
    }
  }

  function intlOptions(opts) {
    var out = {};
    for (var k in opts) if (Object.prototype.hasOwnProperty.call(opts, k)) out[k] = opts[k];
    if (current === "UTC") out.timeZone = "UTC";
    return out;
  }

  function formatter(opts) {
    var key = JSON.stringify(opts);
    var f = fmtCache[key];
    if (!f) {
      f = new Intl.DateTimeFormat(undefined, intlOptions(opts));
      fmtCache[key] = f;
    }
    return f;
  }

  var PARTS_OPTS = {
    year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit",
    hour12: false,
  };

  // formatTimestamp renders an ISO/Date as "YYYY-MM-DD HH:MM:SS" in the
  // configured timezone — the same shape Go's server template emits.
  function formatTimestamp(iso) {
    if (!iso) return "";
    var d = iso instanceof Date ? iso : new Date(iso);
    if (isNaN(d.getTime())) return String(iso);
    var p = parts(d);
    return p.year + "-" + p.month + "-" + p.day + " " + p.hour + ":" + p.minute + ":" + p.second;
  }

  // Intl.DateTimeFormat is the only stable way to read date parts in a
  // non-system timezone — Date.getHours() etc. always read local.
  function parts(d) {
    var f = formatter(PARTS_OPTS);
    var out = {};
    f.formatToParts(d).forEach(function (p) {
      if (p.type !== "literal") out[p.type] = p.value;
    });
    // hour comes back as "24" at midnight in some locales; normalise to "00".
    if (out.hour === "24") out.hour = "00";
    return out;
  }

  document.addEventListener("DOMContentLoaded", function () {
    syncButtons(current);
    var u = document.getElementById("tz-utc");
    var l = document.getElementById("tz-local");
    if (u) u.addEventListener("click", function () { set("UTC"); });
    if (l) l.addEventListener("click", function () { set("local"); });
  });

  return {
    get: get, set: set, onChange: onChange,
    formatter: formatter, parts: parts, formatTimestamp: formatTimestamp,
  };
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
    var displayTs = window.__tz.formatTimestamp(ts);
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

  function timeFmt() {
    return window.__tz.formatter({
      month: "short", day: "numeric", hour: "numeric", minute: "2-digit",
    });
  }

  function formatRange(from, to) {
    var f = timeFmt();
    return f.format(from) + " – " + f.format(to);
  }

  // ── Calendar (month grid) ────────────────────────────────────────
  var MONTH_NAMES = ["January", "February", "March", "April", "May", "June",
    "July", "August", "September", "October", "November", "December"];

  function startOfDay(d) {
    return new Date(d.getFullYear(), d.getMonth(), d.getDate());
  }
  function sameDay(a, b) {
    return a && b && a.getFullYear() === b.getFullYear() &&
      a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
  }
  function combineDateAndTime(date, hhmm) {
    var d = new Date(date.getFullYear(), date.getMonth(), date.getDate(), 0, 0, 0, 0);
    if (typeof hhmm === "string" && /^\d{1,2}:\d{2}$/.test(hhmm)) {
      var parts = hhmm.split(":");
      d.setHours(parseInt(parts[0], 10) || 0, parseInt(parts[1], 10) || 0, 0, 0);
    }
    return d;
  }
  function formatRangeSummary(from, to) {
    if (!from || !to) return "";
    var f = timeFmt();
    return f.format(from) + " – " + f.format(to);
  }

  function wirePicker() {
    var trigger = document.getElementById("picker-trigger");
    var input = document.getElementById("picker-input");
    var caret = document.getElementById("picker-caret");
    var popover = document.getElementById("picker-popover");
    if (!trigger || !popover || !input) return;

    var viewPresets = document.getElementById("picker-view-presets");
    var viewCal = document.getElementById("picker-view-calendar");

    // Calendar state: month being viewed, selected range (start/end dates),
    // and time strings ("HH:MM"). Range is built click-by-click: first click
    // sets start and clears end; second click sets end (swapping if before
    // start); a third click restarts the range from that day.
    var calMonth = startOfDay(new Date());
    calMonth.setDate(1);
    var calStart = null;
    var calEnd = null;
    var fromTimeEl = document.getElementById("cal-from-time");
    var toTimeEl = document.getElementById("cal-to-time");
    var titleEl = document.getElementById("cal-title");
    var gridEl = document.getElementById("cal-grid");
    var summaryEl = document.getElementById("cal-range-summary");

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
      // Seed from any active range in the hidden since/until fields so the
      // operator returns to where they left off.
      var since = document.getElementById("f-since");
      var until = document.getElementById("f-until");
      if (since && since.value && until && until.value) {
        var s = new Date(since.value);
        var u = new Date(until.value);
        if (!isNaN(s.getTime()) && !isNaN(u.getTime())) {
          calStart = startOfDay(s);
          calEnd = startOfDay(u);
          calMonth = new Date(s.getFullYear(), s.getMonth(), 1);
          if (fromTimeEl) fromTimeEl.value = pad2(s.getHours()) + ":" + pad2(s.getMinutes());
          if (toTimeEl) toTimeEl.value = pad2(u.getHours()) + ":" + pad2(u.getMinutes());
        }
      }
      renderCalendar();
    }
    function pad2(n) { return n < 10 ? "0" + n : "" + n; }

    function renderCalendar() {
      if (!gridEl || !titleEl) return;
      titleEl.textContent = MONTH_NAMES[calMonth.getMonth()] + " " + calMonth.getFullYear();
      gridEl.innerHTML = "";
      var firstDow = new Date(calMonth.getFullYear(), calMonth.getMonth(), 1).getDay();
      var daysInMonth = new Date(calMonth.getFullYear(), calMonth.getMonth() + 1, 0).getDate();
      var prevDaysInMonth = new Date(calMonth.getFullYear(), calMonth.getMonth(), 0).getDate();
      var today = startOfDay(new Date());
      var future = startOfDay(new Date());
      future.setDate(future.getDate() + 1);

      // Render 6 weeks (42 cells) to keep height stable across months.
      for (var i = 0; i < 42; i++) {
        var dayNum;
        var cellMonth = calMonth.getMonth();
        var cellYear = calMonth.getFullYear();
        var outside = false;
        if (i < firstDow) {
          dayNum = prevDaysInMonth - (firstDow - i - 1);
          cellMonth -= 1;
          if (cellMonth < 0) { cellMonth = 11; cellYear -= 1; }
          outside = true;
        } else if (i >= firstDow + daysInMonth) {
          dayNum = i - firstDow - daysInMonth + 1;
          cellMonth += 1;
          if (cellMonth > 11) { cellMonth = 0; cellYear += 1; }
          outside = true;
        } else {
          dayNum = i - firstDow + 1;
        }
        var cellDate = new Date(cellYear, cellMonth, dayNum);
        var btn = document.createElement("button");
        btn.type = "button";
        btn.className = "cal-cell";
        btn.textContent = String(dayNum);
        btn.setAttribute("role", "gridcell");
        btn.setAttribute("data-date", cellDate.toISOString());
        if (outside) btn.classList.add("is-outside");
        if (cellDate.getTime() > future.getTime()) {
          btn.classList.add("is-disabled");
          btn.disabled = true;
        }
        if (sameDay(cellDate, today)) btn.classList.add("is-today");
        if (calStart && sameDay(cellDate, calStart)) btn.classList.add("is-start");
        if (calEnd && sameDay(cellDate, calEnd)) btn.classList.add("is-end");
        if (calStart && calEnd && cellDate > calStart && cellDate < calEnd) {
          btn.classList.add("in-range");
        }
        gridEl.appendChild(btn);
      }

      if (summaryEl) {
        if (calStart && calEnd) {
          var from = combineDateAndTime(calStart, fromTimeEl && fromTimeEl.value);
          var to = combineDateAndTime(calEnd, toTimeEl && toTimeEl.value);
          summaryEl.textContent = formatRangeSummary(from, to);
        } else if (calStart) {
          summaryEl.textContent = timeFmt().format(combineDateAndTime(calStart, fromTimeEl && fromTimeEl.value)) + " – …";
        } else {
          summaryEl.textContent = "";
        }
      }
    }

    function handleCellClick(target) {
      var iso = target.getAttribute("data-date");
      if (!iso) return;
      var d = startOfDay(new Date(iso));
      if (!calStart || (calStart && calEnd)) {
        calStart = d; calEnd = null;
      } else if (d < calStart) {
        calEnd = calStart;
        calStart = d;
      } else {
        calEnd = d;
      }
      renderCalendar();
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

    var prev = document.getElementById("cal-prev");
    if (prev) {
      prev.addEventListener("click", function (e) {
        e.stopPropagation();
        calMonth = new Date(calMonth.getFullYear(), calMonth.getMonth() - 1, 1);
        renderCalendar();
      });
    }
    var next = document.getElementById("cal-next");
    if (next) {
      next.addEventListener("click", function (e) {
        e.stopPropagation();
        calMonth = new Date(calMonth.getFullYear(), calMonth.getMonth() + 1, 1);
        renderCalendar();
      });
    }

    if (gridEl) {
      gridEl.addEventListener("click", function (e) {
        var btn = e.target.closest(".cal-cell");
        if (!btn || btn.disabled) return;
        e.stopPropagation();
        handleCellClick(btn);
      });
    }
    if (fromTimeEl) fromTimeEl.addEventListener("input", renderCalendar);
    if (toTimeEl) toTimeEl.addEventListener("input", renderCalendar);

    var apply = document.getElementById("picker-apply");
    if (apply) {
      apply.addEventListener("click", function (e) {
        e.stopPropagation();
        if (!calStart) return;
        var endDate = calEnd || calStart;
        var from = combineDateAndTime(calStart, fromTimeEl && fromTimeEl.value);
        var to = combineDateAndTime(endDate, toTimeEl && toTimeEl.value);
        if (to < from) { var tmp = from; from = to; to = tmp; }
        applyAbsolute(from.toISOString(), to.toISOString());
      });
    }
    var cancel = document.getElementById("picker-cancel");
    if (cancel) {
      cancel.addEventListener("click", function (e) {
        e.stopPropagation();
        showPresets();
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
    if (spanMs <= 4 * hourMs) return function (d) {
      var p = window.__tz.parts(d);
      return p.hour + ":" + p.minute;
    };
    if (spanMs <= 2 * dayMs) return function (d) {
      var p = window.__tz.parts(d);
      return p.hour + ":00";
    };
    return function (d) {
      var p = window.__tz.parts(d);
      return MONTH_ABBR[parseInt(p.month, 10) - 1] + " " + parseInt(p.day, 10);
    };
  }

  // ── Search box ───────────────────────────────────────────────────
  // Committed chips live in `chipTerms[]`; the text input holds only the
  // in-progress (uncommitted) token. A trailing space (or Enter) commits the
  // parsed token(s) into chips and clears the input — the same model used by
  // Datadog / Kibana / Loki filter bars. The hidden `f-q` mirrors
  // chipsToString(chipTerms) + " " + input.value so non-JS submit works and
  // refresh produces an identical state.

  function escAttr(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }
  function escText(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;");
  }

  function renderChip(c) {
    var classes = "qchip";
    if (c.isFreeform) classes += " is-freeform";
    if (c.isHeader) classes += " is-header";
    if (c.negated) classes += " is-negated";
    var html = '<span class="' + classes + '"' +
      ' data-key="' + escAttr(c.key) + '"' +
      ' data-value="' + escAttr(c.value) + '"' +
      ' data-token="' + escAttr(c.token) + '">';
    if (c.negated) html += '<span class="qneg" aria-label="negated">−</span>';
    if (c.isHeader) html += '<span class="qhdr" aria-label="header" title="per-header search">H</span>';
    if (c.isFreeform) html += '<span class="qany" aria-label="any field" title="match against any field">any</span>';
    else html += '<span class="qk">' + escText(c.key) + '</span><span class="qop">:</span>';
    html += '<span class="qv">' + escText(c.value) + '</span>';
    html += '<button type="button" class="qx" data-chip-remove="' + escAttr(c.token) + '" aria-label="Remove">×</button>';
    html += '</span>';
    return html;
  }

  function chipsToString(chipTerms) {
    var sql = window.__searchql;
    if (!sql || !chipTerms || !chipTerms.length) return "";
    var parts = [];
    for (var i = 0; i < chipTerms.length; i++) {
      parts.push(sql.chipFromTerm(chipTerms[i]).token);
    }
    return parts.join(" ");
  }

  function combinedQuery(chipTerms, inputValue) {
    var head = chipsToString(chipTerms);
    var tail = (inputValue || "").trim();
    if (head && tail) return head + " " + tail;
    return head || tail;
  }

  var OR_HINT_DISMISSED_KEY = "httpcatch.orHintDismissed";
  function orHintDismissed() {
    try { return sessionStorage.getItem(OR_HINT_DISMISSED_KEY) === "1"; }
    catch (e) { return false; }
  }
  function dismissOrHint() {
    try { sessionStorage.setItem(OR_HINT_DISMISSED_KEY, "1"); } catch (e) {}
  }

  function wireSearch() {
    var input = document.getElementById("search-input");
    var clear = document.getElementById("search-clear");
    var form = document.getElementById("filter-form");
    var qHidden = document.getElementById("f-q");
    var host = document.getElementById("query-input-host");
    if (!input || !form || !qHidden) return;

    if (!window.__searchql) {
      // Without the JS parser there's nothing more to wire — server-rendered
      // chips remain visible and the form submits via the verbatim hidden q.
      return;
    }
    var sql = window.__searchql;
    var chipTerms = [];
    // -1 means the caret is in the text input; otherwise it indexes chipTerms.
    var selectedChipIndex = -1;

    function clampSelection() {
      if (selectedChipIndex < -1) selectedChipIndex = -1;
      if (selectedChipIndex > chipTerms.length - 1) selectedChipIndex = -1;
    }

    function tokenAt(idx) {
      return sql.chipFromTerm(chipTerms[idx]).token;
    }

    function indexOfChip(chipEl) {
      var siblings = host.querySelectorAll(".qchip");
      for (var i = 0; i < siblings.length; i++) {
        if (siblings[i] === chipEl) return i;
      }
      return -1;
    }

    function applySelectionClasses() {
      var chips = host.querySelectorAll(".qchip");
      for (var i = 0; i < chips.length; i++) {
        chips[i].classList.toggle("is-selected", i === selectedChipIndex);
      }
    }

    function selectChip(idx) {
      selectedChipIndex = idx;
      clampSelection();
      applySelectionClasses();
      if (selectedChipIndex === -1) {
        input.focus();
      } else {
        // Blur the input so its caret stops blinking while a chip is selected;
        // the chip list itself is not focusable.
        input.blur();
      }
    }

    function expandChipToInput(idx) {
      if (idx < 0 || idx >= chipTerms.length) return;
      var token = tokenAt(idx);
      chipTerms.splice(idx, 1);
      selectedChipIndex = -1;
      rerenderChips();
      input.value = token;
      input.focus();
      try { input.setSelectionRange(token.length, token.length); } catch (e) {}
      syncHiddenQ();
      syncBannerAndHint();
    }

    function rerenderChips() {
      var existing = host.querySelectorAll(".qchip");
      for (var i = 0; i < existing.length; i++) existing[i].parentNode.removeChild(existing[i]);
      clampSelection();
      if (!chipTerms.length) return;
      var html = "";
      for (var j = 0; j < chipTerms.length; j++) {
        html += renderChip(sql.chipFromTerm(chipTerms[j]));
      }
      input.insertAdjacentHTML("beforebegin", html);
      applySelectionClasses();
    }

    function syncBannerAndHint() {
      var combined = sql.parseQuery(combinedQuery(chipTerms, input.value));
      var banner = document.getElementById("scan-banner");
      if (banner) {
        if (sql.isUnindexedScan(combined)) banner.removeAttribute("hidden");
        else banner.setAttribute("hidden", "");
      }
      var hint = document.getElementById("or-hint");
      if (hint) {
        var show = sql.shouldShowOrHint(combined) >= 0 && !orHintDismissed();
        if (show) hint.removeAttribute("hidden");
        else hint.setAttribute("hidden", "");
      }
    }

    function syncHiddenQ() {
      qHidden.value = combinedQuery(chipTerms, input.value);
    }

    function commitAll() {
      var raw = input.value.trim();
      if (!raw) { input.value = ""; return; }
      var parsed = sql.parseQuery(raw);
      if (parsed.error) return;
      for (var i = 0; i < parsed.terms.length; i++) chipTerms.push(parsed.terms[i]);
      input.value = "";
      rerenderChips();
    }

    // Hydrate chip state from the server-rendered q. Server-rendered chips
    // are replaced with the JS-managed set so the DOM and JS state agree.
    if (qHidden.value) {
      var hydrate = sql.parseQuery(qHidden.value);
      if (!hydrate.error) {
        chipTerms = hydrate.terms.slice();
      }
    }
    input.value = "";
    rerenderChips();
    syncHiddenQ();
    syncBannerAndHint();
    if (clear) clear.hidden = true;

    input.addEventListener("input", function () {
      if (clear) clear.hidden = input.value === "";
      // Trailing space commits the parsed token(s) before the space and
      // clears the input. Quoted strings with internal whitespace stay
      // pending until the closing quote arrives (tokeniser bails on the
      // unclosed quote, so commitAll() is a no-op until the value parses).
      var raw = input.value;
      var committed = false;
      if (/\s$/.test(raw)) {
        var head = raw.replace(/\s+$/, "");
        if (head !== "") {
          var parsed = sql.parseQuery(head);
          if (!parsed.error) {
            for (var i = 0; i < parsed.terms.length; i++) chipTerms.push(parsed.terms[i]);
            input.value = "";
            rerenderChips();
            committed = true;
            if (clear) clear.hidden = true;
          }
        } else {
          input.value = "";
        }
      } else {
        // Mid-typing: commit any tokens fully separated by whitespace,
        // keep the still-being-typed last token in the input. Bail on
        // parse error (e.g. partial `service:`) — user is still editing.
        var parsedMid = sql.parseQuery(raw);
        if (!parsedMid.error && parsedMid.terms.length >= 2) {
          var tokens = sql.tokenize(raw).tokens || [];
          if (tokens.length === parsedMid.terms.length) {
            for (var k = 0; k < parsedMid.terms.length - 1; k++) chipTerms.push(parsedMid.terms[k]);
            input.value = tokens[tokens.length - 1];
            rerenderChips();
            committed = true;
          }
        }
      }
      syncHiddenQ();
      syncBannerAndHint();
      if (committed) {
        input.focus();
        var len = input.value.length;
        try { input.setSelectionRange(len, len); } catch (e) {}
      }
    });

    if (clear) {
      clear.addEventListener("click", function () {
        input.value = "";
        clear.hidden = true;
        input.focus();
        syncHiddenQ();
        syncBannerAndHint();
      });
    }

    input.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        e.preventDefault();
        commitAll();
        syncHiddenQ();
        submitForm();
        return;
      }
      if (e.key === "Backspace" && input.value === "" && chipTerms.length > 0) {
        e.preventDefault();
        chipTerms.pop();
        rerenderChips();
        syncHiddenQ();
        syncBannerAndHint();
        input.focus();
        submitForm();
        return;
      }
      if ((e.key === "ArrowLeft" || e.key === "ArrowUp") &&
          input.value === "" && chipTerms.length > 0) {
        e.preventDefault();
        // Stop propagation so the document-level handler doesn't immediately
        // shift the just-set selection one position to the left.
        e.stopPropagation();
        selectChip(chipTerms.length - 1);
      }
    });

    // Listens at document level because chip navigation runs while the input
    // is blurred — input-scoped listeners would miss those events.
    document.addEventListener("keydown", function (e) {
      if (selectedChipIndex < 0) return;
      // Don't hijack typing into another input/textarea.
      var t = e.target;
      if (t && t !== document.body && t !== input &&
          (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) {
        return;
      }
      if (e.key === "ArrowLeft") {
        e.preventDefault();
        if (selectedChipIndex > 0) selectChip(selectedChipIndex - 1);
        return;
      }
      if (e.key === "ArrowRight") {
        e.preventDefault();
        if (selectedChipIndex < chipTerms.length - 1) selectChip(selectedChipIndex + 1);
        else selectChip(-1);
        return;
      }
      if (e.key === "Enter") {
        e.preventDefault();
        expandChipToInput(selectedChipIndex);
        return;
      }
      if (e.key === "Backspace" || e.key === "Delete") {
        e.preventDefault();
        var removeAt = selectedChipIndex;
        chipTerms.splice(removeAt, 1);
        // After delete, leave selection on the chip that took its place,
        // or fall back to the previous chip / the input.
        if (removeAt < chipTerms.length) {
          selectedChipIndex = removeAt;
        } else if (chipTerms.length > 0) {
          selectedChipIndex = chipTerms.length - 1;
        } else {
          selectedChipIndex = -1;
        }
        rerenderChips();
        syncHiddenQ();
        syncBannerAndHint();
        if (selectedChipIndex === -1) input.focus();
        submitForm();
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        selectChip(-1);
        return;
      }
      // Single printable key: expand the selected chip into the input and
      // append the typed character. e.key.length === 1 filters out named keys
      // (F1, ArrowUp, etc.) so chip navigation isn't hijacked.
      if (e.key && e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault();
        var idx = selectedChipIndex;
        var token = tokenAt(idx);
        chipTerms.splice(idx, 1);
        selectedChipIndex = -1;
        rerenderChips();
        input.value = token + e.key;
        input.focus();
        var len = input.value.length;
        try { input.setSelectionRange(len, len); } catch (_) {}
        syncHiddenQ();
        syncBannerAndHint();
      }
    });

    form.addEventListener("submit", function () {
      commitAll();
      syncHiddenQ();
    });

    host.addEventListener("click", function (e) {
      var removeBtn = e.target.closest("[data-chip-remove]");
      if (removeBtn) {
        e.preventDefault();
        var chipR = removeBtn.closest(".qchip");
        if (!chipR) return;
        var indexR = indexOfChip(chipR);
        if (indexR < 0) return;
        chipTerms.splice(indexR, 1);
        selectedChipIndex = -1;
        rerenderChips();
        syncHiddenQ();
        syncBannerAndHint();
        input.focus();
        submitForm();
        return;
      }
      var chip = e.target.closest(".qchip");
      if (chip) {
        var idx = indexOfChip(chip);
        if (idx >= 0) {
          e.preventDefault();
          expandChipToInput(idx);
        }
      }
    });

    input.addEventListener("focus", function () {
      if (selectedChipIndex !== -1) selectChip(-1);
    });

    var hintDismiss = document.getElementById("or-hint-dismiss");
    if (hintDismiss) {
      hintDismiss.addEventListener("click", function () {
        dismissOrHint();
        var hint = document.getElementById("or-hint");
        if (hint) hint.setAttribute("hidden", "");
      });
    }
  }

  // ── Saved views (localStorage) ───────────────────────────────────
  var SAVED_KEY = "httpcatch.savedViews";

  // PRE_CUTOVER_FIELD_KEYS lists the per-field URL parameters that the
  // pre-q saved views used. Anything in this set or matching `header.<name>`
  // gets folded into a single `q=` token on load.
  var PRE_CUTOVER_FIELD_KEYS = {
    service: 1, host: 1, path: 1, method: 1, status: 1, source_ip: 1,
    correlation_id: 1, body: 1, headers: 1,
  };
  var TEMPORAL_PARAMS = ["since", "until", "limit", "live"];

  function migrateSavedView(view) {
    if (!view || typeof view !== "object" || typeof view.query !== "string") return null;
    var params;
    try { params = new URLSearchParams(view.query); }
    catch (e) { return null; }

    var hasOld = false;
    var oldTokens = [];
    var entries = [];
    params.forEach(function (v, k) { entries.push([k, v]); });
    for (var i = 0; i < entries.length; i++) {
      var k = entries[i][0];
      if (PRE_CUTOVER_FIELD_KEYS[k] || k.indexOf("header.") === 0) {
        hasOld = true;
        oldTokens.push(k + ":" + entries[i][1]);
      }
    }
    if (!hasOld) return view;

    var qExisting = params.get("q") || "";
    var merged = (qExisting ? [qExisting] : []).concat(oldTokens).join(" ");
    if (window.__searchql) {
      var parsed = window.__searchql.parseQuery(merged);
      if (parsed.error) {
        console.warn("httpcatch: discarding unrecognisable saved view", view, parsed.error);
        return null;
      }
    }
    var next = new URLSearchParams();
    for (var j = 0; j < TEMPORAL_PARAMS.length; j++) {
      var tk = TEMPORAL_PARAMS[j];
      if (params.get(tk)) next.set(tk, params.get(tk));
    }
    next.set("q", merged);
    return { name: view.name, query: next.toString() };
  }

  function loadSaved() {
    try {
      var raw = localStorage.getItem(SAVED_KEY);
      if (!raw) return [];
      var arr = JSON.parse(raw);
      if (!Array.isArray(arr)) return [];
      var out = [];
      var dirty = false;
      for (var i = 0; i < arr.length; i++) {
        var migrated = migrateSavedView(arr[i]);
        if (!migrated) { dirty = true; continue; }
        if (migrated !== arr[i]) dirty = true;
        out.push(migrated);
      }
      if (dirty) storeSaved(out);
      return out;
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

  // The Go template server-renders [data-timestamp] elements in UTC, so this
  // is a no-op on first paint when the operator has TZ=UTC selected.
  function reformatTimestamps(root) {
    (root || document).querySelectorAll("[data-timestamp]").forEach(function (el) {
      var iso = el.getAttribute("data-timestamp");
      if (!iso) return;
      var formatted = window.__tz.formatTimestamp(iso);
      var link = el.querySelector("a.row-link");
      var target = link || el;
      target.textContent = formatted;
    });
  }

  // ── Bootstrapping ────────────────────────────────────────────────
  document.addEventListener("DOMContentLoaded", function () {
    refreshTriggerLabel();
    wirePicker();
    wireSearch();
    wireSidePanel();
    wireSavedViews();
    wireExport();
    reformatTimestamps(document);
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

    document.addEventListener("httpcatch:rows-updated", function () {
      reformatTimestamps(document);
      renderHistogramAndCount();
    });

    window.__tz.onChange(function () {
      reformatTimestamps(document);
      refreshTriggerLabel();
      renderHistogramAndCount();
    });

    wireCopyButtons(document);
  });

  function wireCopyButtons(root) {
    var buttons = root.querySelectorAll("[data-copy-target]");
    buttons.forEach(function (btn) {
      btn.addEventListener("click", function () {
        var target = document.getElementById(btn.getAttribute("data-copy-target"));
        if (!target) return;
        navigator.clipboard.writeText(target.textContent || "").then(function () {
          var prev = btn.textContent;
          btn.textContent = "Copied";
          btn.classList.add("is-copied");
          setTimeout(function () {
            btn.textContent = prev;
            btn.classList.remove("is-copied");
          }, 1200);
        });
      });
    });
  }
})();
