(function () {
  var handle = null;
  var INTERVAL_MS = 5000;

  function setText(selector, text) {
    var el = document.querySelector(selector);
    if (el) el.textContent = text;
  }

  function setHidden(id, hidden) {
    var el = document.getElementById(id);
    if (!el) return;
    if (hidden) {
      el.setAttribute("hidden", "");
    } else {
      el.removeAttribute("hidden");
    }
  }

  function setCount(id, n) {
    var el = document.getElementById(id);
    if (!el) return;
    var span = el.querySelector("[data-count]");
    if (span) span.textContent = n;
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

        setHidden("banner-unredacted", !data.unredacted);

        var dropped = data.counters.dropped_total;
        setHidden("banner-dropped", dropped <= 0);
        setCount("banner-dropped", dropped);

        var redactionErrors = data.counters.redaction_errors_total;
        setHidden("banner-redaction-errors", redactionErrors <= 0);
        setCount("banner-redaction-errors", redactionErrors);

        var serviceCount = data.counters.captured_without_service_total;
        setHidden("chip-service", serviceCount <= 0);
        setCount("chip-service", serviceCount);

        var corrCount = data.counters.captured_without_correlation_total;
        setHidden("chip-correlation", corrCount <= 0);
        setCount("chip-correlation", corrCount);

        setText("#chip-version", data.version);
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
    initCurlCopy();
  });

  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "visible") {
      refresh();
      startPolling();
    } else {
      stopPolling();
    }
  });

  function shellEscape(s) {
    // Wrap in single quotes and escape any single quotes within the value.
    return "'" + s.replace(/'/g, "'\\''") + "'";
  }

  function initCurlCopy() {
    var btn = document.getElementById("curl-copy-btn");
    if (!btn) return;
    btn.removeAttribute("hidden");

    btn.addEventListener("click", function () {
      var method = btn.getAttribute("data-method") || "GET";
      var path = btn.getAttribute("data-path") || "/";
      var headersJSON = btn.getAttribute("data-headers") || "{}";
      var bodyB64 = btn.getAttribute("data-body") || "";

      var headers;
      try {
        headers = JSON.parse(headersJSON);
      } catch (_) {
        headers = {};
      }

      var parts = ["curl", "-X", shellEscape(method)];

      // Headers
      Object.keys(headers).forEach(function (name) {
        var values = headers[name];
        if (Array.isArray(values)) {
          values.forEach(function (v) {
            parts.push("-H", shellEscape(name + ": " + v));
          });
        }
      });

      // Body
      var body = "";
      if (bodyB64) {
        try {
          body = atob(bodyB64);
        } catch (_) {
          body = "";
        }
      }
      if (body) {
        parts.push("--data-raw", shellEscape(body));
      }

      parts.push(shellEscape(path));

      var cmd = parts.join(" ");

      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(cmd).catch(function (err) {
          console.error("httpcatch: clipboard write failed:", err);
        });
      }
    });
  }
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
      '<tr class="' + escapeHTML(rowClass) + ' live-tail-new" data-timestamp="' + escapeHTML(ts) + '">' +
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

    // Reveal the toggle — non-JS users never see it because it starts hidden.
    toggle.removeAttribute("hidden");

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

          var decision = decideReplaceOrPrepend(records.length);

          var html = "";
          for (var i = 0; i < records.length; i++) {
            html += rowHTML(records[i]);
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
