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
