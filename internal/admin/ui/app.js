function shellEscape(s) {
  return "'" + s.replace(/'/g, "'\\''") + "'";
}

// initCurlCopy wires the "Copy as cURL" button found within root (defaults to
// document). Safe to call after the detail markup is re-rendered into a new
// container; the button must carry data-method/data-path/data-headers/data-body.
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

(function () {
  var handle = null;
  var INTERVAL_MS = 5000;

  var lastApplied = {};

  function setText(selector, text) {
    if (lastApplied[selector] === text) return;
    var el = document.querySelector(selector);
    if (!el) return;
    el.textContent = text;
    lastApplied[selector] = text;
  }

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
    if (span) span.textContent = n;
    lastApplied[key] = n;
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

// Live tail — progressive enhancement layer.
// Exposes pure helpers on window.__livetail for unit testing without a browser.
(function () {
  var POLL_INTERVAL_MS = 2000;
  var MAX_PREPEND = 50;

  // deriveSince returns the data-timestamp value of the topmost row element,
  // or an empty string when no rows carry the attribute.
  function deriveSince(rows) {
    for (var i = 0; i < rows.length; i++) {
      var ts = rows[i].getAttribute && rows[i].getAttribute("data-timestamp");
      if (ts) return ts;
    }
    return "";
  }

  // decideReplaceOrPrepend returns 'replace' when newRowCount exceeds
  // MAX_PREPEND, otherwise 'prepend'.
  function decideReplaceOrPrepend(newRowCount) {
    if (newRowCount > MAX_PREPEND) return "replace";
    return "prepend";
  }

  // buildPollURL constructs the URL for a single poll tick.
  // It strips cursor= from the current query string, replaces since= with the
  // derived since value, and appends limit=50.
  function buildPollURL(since) {
    var params = new URLSearchParams(window.location.search);
    params.delete("cursor");
    params.delete("since");
    if (since) params.set("since", since);
    params.set("limit", "50");
    return "/requests?" + params.toString();
  }

  // rowHTML converts a single RootRow JSON object from the inspect API into an
  // HTML string that matches the server-rendered row shape.
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

    var eventCountText =
      row.event_count !== null && row.event_count !== undefined
        ? String(row.event_count)
        : "&mdash;";
    var statusText =
      row.status !== null && row.status !== undefined ? String(row.status) : "";

    return (
      '<tr class="' + escapeHTML(rowClass) + ' live-tail-new" data-timestamp="' + escapeHTML(ts) + '" data-id="' + escapeHTML(id) + '">' +
      "<td>" + link(href, displayTs) + "</td>" +
      "<td>" + badgeHTML + "</td>" +
      "<td>" + link(href, escapeHTML(row.service || "")) + "</td>" +
      "<td>" + link(href, escapeHTML(row.method || "")) + "</td>" +
      "<td>" + link(href, escapeHTML(row.path || "")) + "</td>" +
      "<td>" + link(href, eventCountText) + "</td>" +
      "<td>" + link(href, escapeHTML(statusText)) + "</td>" +
      "</tr>"
    );
  }

  function link(href, content) {
    return '<a href="' + href + '" class="row-link">' + content + "</a>";
  }

  function escapeHTML(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  // mergePrepend inserts newRowsHTML at the top of tbody. Removes the empty-state
  // row if present.
  function mergePrepend(tbody, newRowsHTML) {
    // Remove empty-state placeholder if present.
    var empty = tbody.querySelector(".empty-state");
    if (empty && empty.parentNode) {
      empty.parentNode.removeChild(empty);
    }
    tbody.insertAdjacentHTML("afterbegin", newRowsHTML);
  }

  // Expose pure helpers for Go-level structural and behavioural tests.
  window.__livetail = {
    deriveSince: deriveSince,
    decideReplaceOrPrepend: decideReplaceOrPrepend,
    buildPollURL: buildPollURL,
    rowHTML: rowHTML,
    mergePrepend: mergePrepend,
  };

  // ── DOM init ────────────────────────────────────────────────────────────────

  document.addEventListener("DOMContentLoaded", function () {
    var toggle = document.getElementById("live-tail-toggle");
    if (!toggle) return; // Not on the list page.

    var checkbox = document.getElementById("live-tail-checkbox");
    var statusDiv = document.getElementById("live-tail-status");
    var tbody = document.getElementById("requests-tbody");
    var paginationNav = document.getElementById("pagination-nav");

    var intervalHandle = null;
    var failureCount = 0;
    var active = false;

    // Sync toggle visual state to the active flag.
    function syncCheckbox() {
      checkbox.checked = active;
    }

    // Read hash to decide initial state.
    function readHash() {
      return window.location.hash.indexOf("live=1") !== -1;
    }

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

    // setPaginationDisabled adds or removes aria-disabled on all page-link
    // anchors inside the pagination nav.
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

    // Intercept clicks on disabled pagination links.
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
          if (!res.ok) {
            throw new Error("HTTP " + res.status);
          }
          return res.json();
        })
        .then(function (data) {
          failureCount = 0;
          clearStatus();

          var records = data.records || [];
          if (records.length === 0) return;

          // Dedupe against the rows already in the table. The server's
          // since filter is inclusive (>= since), so the topmost row often
          // comes back unchanged each tick — drop those before rendering.
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
          for (var i = 0; i < fresh.length; i++) {
            html += rowHTML(fresh[i]);
          }

          if (decision === "replace") {
            tbody.innerHTML = html;
          } else {
            mergePrepend(tbody, html);
          }
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
      poll(); // immediate fetch on activation
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

    // Retry from the status message.
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
      if (checkbox.checked) {
        activate();
      } else {
        deactivate();
      }
    });

    // Page Visibility API: pause when hidden, resume when visible.
    document.addEventListener("visibilitychange", function () {
      if (!active) return;
      if (document.visibilityState === "visible") {
        start();
        poll(); // backfill missed records
      } else {
        stop();
      }
    });

    // Initialise from hash.
    if (readHash()) {
      activate();
    }
  });
})();

/* ════════════════════════════════════════════════════════════════════
   Search box, time-range picker, side-panel detail, and histogram.
   ════════════════════════════════════════════════════════════════════ */
(function () {
  // ── Shorthand parsing ───────────────────────────────────────────────

  // parseShorthand turns "3d" / "15m" / "1h" / "1w" / "1mo" into milliseconds.
  // Returns 0 on invalid input.
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

  // ── Range application ──────────────────────────────────────────────

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
  }

  function submitForm() {
    var form = document.getElementById("filter-form");
    if (!form) return;
    form.submit();
  }

  // ── Label helpers ──────────────────────────────────────────────────

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

  // ── Picker wiring ──────────────────────────────────────────────────

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
      // Pre-fill calendar inputs from current since/until.
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

    // Type a shorthand directly into the trigger.
    input.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        e.preventDefault();
        var raw = input.value.trim();
        var ms = parseShorthand(raw);
        if (ms > 0) {
          applyPreset(raw);
        }
        return;
      }
      if (e.key === "Escape") {
        closePopover();
        input.blur();
      }
    });

    // Preset clicks (presets and live-tail entry).
    viewPresets.addEventListener("click", function (e) {
      var btn = e.target.closest("button[data-preset]");
      if (!btn) return;
      e.stopPropagation();
      applyPreset(btn.getAttribute("data-preset"));
    });

    // "Select from calendar…" drills into the calendar view.
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

  // Compute and set the trigger label from current since/until (or live).
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

  // ── Side-panel detail ──────────────────────────────────────────────

  function wireSidePanel() {
    var panel = document.getElementById("detail-panel");
    var scrim = document.getElementById("detail-panel-scrim");
    var closeBtn = document.getElementById("detail-panel-close");
    var expandLink = document.getElementById("detail-panel-expand");
    var body = document.getElementById("detail-panel-body");
    var tbody = document.getElementById("requests-tbody");
    if (!panel || !tbody) return;

    var currentID = null;

    function openWith(id, url) {
      currentID = id;
      panel.removeAttribute("hidden");
      scrim.removeAttribute("hidden");
      if (expandLink) expandLink.setAttribute("href", url);
      body.innerHTML = '<div class="detail-panel-loading">Loading…</div>';

      // Mark the row visually.
      tbody.querySelectorAll("tr.row-active").forEach(function (r) {
        r.classList.remove("row-active");
      });
      var row = tbody.querySelector('tr[data-id="' + cssEscape(id) + '"]');
      if (row) row.classList.add("row-active");

      // Fetch the rendered HTML detail page and extract the relevant sections.
      fetch(url, { credentials: "same-origin", headers: { Accept: "text/html" } })
        .then(function (res) {
          if (!res.ok) throw new Error("HTTP " + res.status);
          return res.text();
        })
        .then(function (html) {
          if (currentID !== id) return; // user opened another row first
          var doc = new DOMParser().parseFromString(html, "text/html");
          var detail = doc.querySelector(".detail-section");
          var timeline = doc.querySelector(".timeline-section");
          body.innerHTML = "";
          if (detail) body.appendChild(document.adoptNode(detail));
          if (timeline) body.appendChild(document.adoptNode(timeline));
          initCurlCopy(body);
        })
        .catch(function (err) {
          if (currentID !== id) return;
          body.innerHTML = '<div class="inline-error">Failed to load: ' + (err && err.message) + "</div>";
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

    // Intercept row-link clicks (only inside the requests tbody).
    tbody.addEventListener("click", function (e) {
      var link = e.target.closest("a.row-link");
      if (!link) return;
      // Allow modifier-clicks to open in a new tab as usual.
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
      var tr = link.closest("tr[data-id]");
      if (!tr) return;
      e.preventDefault();
      openWith(tr.getAttribute("data-id"), link.getAttribute("href"));
    });
  }

  function cssEscape(s) {
    if (window.CSS && CSS.escape) return CSS.escape(s);
    return String(s).replace(/[^a-zA-Z0-9_-]/g, "\\$&");
  }

  // ── Histogram and request count ────────────────────────────────────

  function renderHistogramAndCount() {
    var section = document.getElementById("histogram-section");
    var canvas = document.getElementById("histogram-canvas");
    var countEl = document.getElementById("histogram-count");
    var rangeEl = document.getElementById("histogram-range");
    var tbody = document.getElementById("requests-tbody");
    if (!section || !canvas || !countEl || !tbody) return;

    var rows = tbody.querySelectorAll("tr[data-timestamp]");
    var count = rows.length;
    countEl.textContent = count === 1 ? "1 request" : (count.toLocaleString() + " requests");

    var since = document.getElementById("f-since");
    var until = document.getElementById("f-until");
    if (since && until && since.value && until.value) {
      var s = new Date(since.value);
      var u = new Date(until.value);
      if (rangeEl && !isNaN(s.getTime()) && !isNaN(u.getTime())) {
        rangeEl.textContent = "· " + formatRange(s, u);
      }
    }

    if (count === 0) {
      // Clear the canvas.
      var ctx = canvas.getContext("2d");
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      return;
    }

    // Collect timestamps.
    var timestamps = [];
    rows.forEach(function (r) {
      var ts = r.getAttribute("data-timestamp");
      var d = new Date(ts);
      if (!isNaN(d.getTime())) timestamps.push(d.getTime());
    });
    if (timestamps.length === 0) return;

    var minT = Math.min.apply(null, timestamps);
    var maxT = Math.max.apply(null, timestamps);
    // Snap to since/until if available — gives proper empty buckets at the edges.
    if (since && since.value) {
      var sd = new Date(since.value); if (!isNaN(sd.getTime())) minT = sd.getTime();
    }
    if (until && until.value) {
      var ud = new Date(until.value); if (!isNaN(ud.getTime())) maxT = ud.getTime();
    }
    if (maxT <= minT) maxT = minT + 60000;

    var buckets = 40;
    var bucketSize = (maxT - minT) / buckets;
    var counts = new Array(buckets).fill(0);
    timestamps.forEach(function (t) {
      var i = Math.floor((t - minT) / bucketSize);
      if (i < 0) i = 0;
      if (i >= buckets) i = buckets - 1;
      counts[i]++;
    });

    drawHistogram(canvas, counts, minT, maxT);
  }

  var AXIS_HEIGHT = 18;

  function drawHistogram(canvas, counts, minT, maxT) {
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
    counts.forEach(function (c) { if (c > max) max = c; });

    // Bars first (so the axis baseline draws over them).
    if (max > 0) {
      var n = counts.length;
      var barW = w / n;
      var gap = Math.max(1, barW * 0.15);
      ctx.fillStyle = "#3b82f6";
      counts.forEach(function (c, i) {
        if (c === 0) return;
        var barH = (c / max) * (chartH - 4);
        var x = i * barW + gap / 2;
        var y = chartH - barH;
        ctx.fillRect(x, y, Math.max(1, barW - gap), barH);
      });
    }

    // Baseline.
    ctx.strokeStyle = "#e4e4e7";
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
    ctx.fillStyle = "#71717a";
    ctx.font = "10px system-ui, -apple-system, sans-serif";
    ctx.textBaseline = "bottom";

    // Tick marks just below the baseline.
    ctx.strokeStyle = "#e4e4e7";
    ctx.lineWidth = 1;

    for (var i = 0; i <= ticks; i++) {
      var t = minT + (spanMs * i / ticks);
      var x = (w * i / ticks);
      var label = fmt(new Date(t));
      var metrics = ctx.measureText(label);
      var tx = x - metrics.width / 2;
      if (i === 0) tx = 1;
      if (i === ticks) tx = w - metrics.width - 1;
      ctx.fillText(label, tx, h - 2);

      ctx.beginPath();
      ctx.moveTo(Math.min(Math.max(x, 0.5), w - 0.5), chartH);
      ctx.lineTo(Math.min(Math.max(x, 0.5), w - 0.5), chartH + 3);
      ctx.stroke();
    }
  }

  function pad2(n) { return n < 10 ? "0" + n : "" + n; }

  var MONTH_ABBR = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

  function pickAxisFormat(spanMs) {
    var hourMs = 3600 * 1000;
    var dayMs = 24 * hourMs;
    if (spanMs <= 4 * hourMs) {
      return function (d) { return pad2(d.getHours()) + ":" + pad2(d.getMinutes()); };
    }
    if (spanMs <= 2 * dayMs) {
      return function (d) { return pad2(d.getHours()) + ":00"; };
    }
    return function (d) { return MONTH_ABBR[d.getMonth()] + " " + d.getDate(); };
  }

  // ── Kibana-style search box ────────────────────────────────────────

  var SEARCH_KEYS = ["service", "method", "status", "path", "body", "correlation_id", "source_ip"];

  // parseSearch tokenizes a Kibana-style query into structured filter values.
  // Recognised keys: service:, method:, status:, path:, body:, correlation_id:,
  // source_ip:. Tokens without a recognised prefix concatenate into body.
  // Quoted values like path:"/api foo" preserve spaces.
  function parseSearch(s) {
    var out = {};
    SEARCH_KEYS.forEach(function (k) { out[k] = ""; });
    if (!s) return out;
    var freeText = [];
    var tokens = s.match(/(?:[^\s"]+|"[^"]*")+/g) || [];
    tokens.forEach(function (tok) {
      var idx = tok.indexOf(":");
      if (idx <= 0) {
        freeText.push(strip(tok));
        return;
      }
      var key = tok.slice(0, idx).toLowerCase();
      var val = strip(tok.slice(idx + 1));
      if (SEARCH_KEYS.indexOf(key) !== -1) {
        out[key] = val;
      } else {
        freeText.push(tok);
      }
    });
    if (freeText.length && !out.body) {
      out.body = freeText.join(" ").trim();
    }
    return out;
  }

  function strip(tok) {
    if (tok.length >= 2 && tok.charAt(0) === '"' && tok.charAt(tok.length - 1) === '"') {
      return tok.slice(1, -1);
    }
    return tok;
  }

  // buildSearch turns the structured filter values back into a search string,
  // quoting any value containing whitespace.
  function buildSearch(filters) {
    var parts = [];
    SEARCH_KEYS.forEach(function (k) {
      var v = filters[k];
      if (!v) return;
      if (/\s/.test(v)) v = '"' + v + '"';
      parts.push(k + ":" + v);
    });
    return parts.join(" ");
  }

  function wireSearch() {
    var input = document.getElementById("search-input");
    var clear = document.getElementById("search-clear");
    var form = document.getElementById("filter-form");
    if (!input || !form) return;

    // Initialize the search box from the server-rendered hidden-input values.
    var initial = {
      service: valOf("f-service"),
      method: valOf("f-method"),
      status: valOf("f-status"),
      path: valOf("f-path"),
      body: valOf("f-body"),
      correlation_id: valOf("f-correlation-id"),
      source_ip: valOf("f-source-ip"),
    };
    input.value = buildSearch(initial);
    if (clear) clear.hidden = input.value === "";

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

    function flushSearchToMirrors() {
      var parsed = parseSearch(input.value);
      setVal("f-service", parsed.service);
      setVal("f-method", parsed.method);
      setVal("f-status", parsed.status);
      setVal("f-path", parsed.path);
      setVal("f-body", parsed.body);
      setVal("f-correlation-id", parsed.correlation_id);
      setVal("f-source-ip", parsed.source_ip);
    }
  }

  function valOf(id) {
    var el = document.getElementById(id);
    return el ? el.value : "";
  }

  function setVal(id, v) {
    var el = document.getElementById(id);
    if (el) el.value = v;
  }

  document.addEventListener("DOMContentLoaded", function () {
    refreshTriggerLabel();
    wirePicker();
    wireSearch();
    wireSidePanel();
    renderHistogramAndCount();
    var rafHandle = 0;
    window.addEventListener("resize", function () {
      if (rafHandle) return;
      rafHandle = requestAnimationFrame(function () {
        rafHandle = 0;
        renderHistogramAndCount();
      });
    });
  });
})();
