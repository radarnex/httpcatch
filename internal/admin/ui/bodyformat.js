// Body beautification for the request/response detail viewer. Pure functions
// only — DOM wiring (the Raw/Beautified toggle) lives in app.js. Exposed both
// as a CommonJS module (for `node bodyformat.test.js`) and as a browser global
// window.__bodyformat, mirroring searchql.js.
(function () {
  // detect resolves a body to a beautifiable format key, or "" when none
  // applies. The Content-Type is authoritative; when it is absent or generic
  // the first non-whitespace byte is sniffed. form-urlencoded is never sniffed
  // (a bare key=value string is indistinguishable from prose), so it relies on
  // the Content-Type alone.
  function detect(contentType, text) {
    var ct = String(contentType || "").toLowerCase().split(";")[0].trim();

    if (ct === "application/json" || /\+json$/.test(ct)) return "json";
    if (ct === "application/xml" || ct === "text/xml" || /\+xml$/.test(ct)) return "xml";
    if (ct === "text/html") return "html";
    if (ct === "application/x-www-form-urlencoded") return "form";

    if (ct === "" || ct === "text/plain" || ct === "*/*" || ct === "application/octet-stream") {
      var head = String(text || "").replace(/^﻿/, "");
      var i = 0;
      while (i < head.length && /\s/.test(head[i])) i++;
      var c = head[i];
      if (c === "{" || c === "[") return "json";
      if (c === "<") {
        var rest = head.slice(i).toLowerCase();
        if (rest.indexOf("<!doctype html") === 0 || rest.indexOf("<html") === 0) return "html";
        return "xml";
      }
    }
    return "";
  }

  // beautify returns the formatted body, or null when the body cannot be
  // formatted for the given format (e.g. malformed/truncated JSON). A null
  // result signals the caller to keep showing the raw body.
  function beautify(text, format) {
    if (text == null || text === "") return null;
    switch (format) {
      case "json": return beautifyJSON(text);
      case "form": return beautifyForm(text);
      case "xml":  return beautifyMarkup(text, false);
      case "html": return beautifyMarkup(text, true);
      default:     return null;
    }
  }

  function beautifyJSON(text) {
    try {
      return JSON.stringify(JSON.parse(text), null, 2);
    } catch (e) {
      return null;
    }
  }

  // beautifyForm renders each key/value pair on its own line, percent-decoded
  // and with "+" expanded to spaces, so a urlencoded blob reads as a list.
  function beautifyForm(text) {
    var pairs = String(text).split("&");
    var lines = [];
    for (var i = 0; i < pairs.length; i++) {
      var p = pairs[i];
      if (p === "") continue;
      var eq = p.indexOf("=");
      var key = eq === -1 ? p : p.slice(0, eq);
      var val = eq === -1 ? "" : p.slice(eq + 1);
      lines.push(decodeFormComponent(key) + " = " + decodeFormComponent(val));
    }
    return lines.length ? lines.join("\n") : null;
  }

  function decodeFormComponent(s) {
    try {
      return decodeURIComponent(s.replace(/\+/g, " "));
    } catch (e) {
      return s;
    }
  }

  // HTML void elements never have a closing tag, so they must not open a new
  // indentation level.
  var VOID_ELEMENTS = {
    area: 1, base: 1, br: 1, col: 1, embed: 1, hr: 1, img: 1, input: 1,
    link: 1, meta: 1, param: 1, source: 1, track: 1, wbr: 1,
  };

  // beautifyMarkup re-indents XML/HTML by walking a flat token stream. It is
  // deliberately lenient: it never throws on malformed or truncated markup, it
  // just produces best-effort indentation. Returns null only when the input
  // contains no element tags at all.
  function beautifyMarkup(text, isHTML) {
    var tokens = tokenizeMarkup(String(text));
    var hasTag = false;
    for (var t = 0; t < tokens.length; t++) {
      if (tokens[t].kind === "open" || tokens[t].kind === "close" || tokens[t].kind === "self") {
        hasTag = true;
        break;
      }
    }
    if (!hasTag) return null;

    var out = [];
    var depth = 0;
    for (var i = 0; i < tokens.length; i++) {
      var tk = tokens[i];
      if (tk.kind === "close") {
        depth = Math.max(0, depth - 1);
        out.push(pad(depth) + tk.value);
        continue;
      }
      if (tk.kind === "open") {
        if (isHTML && VOID_ELEMENTS[tk.name]) {
          out.push(pad(depth) + tk.value);
          continue;
        }
        // Collapse <tag>text</tag> (element with only text) onto one line.
        var next = tokens[i + 1];
        var after = tokens[i + 2];
        if (next && next.kind === "text" && after && after.kind === "close" && after.name === tk.name) {
          out.push(pad(depth) + tk.value + next.value + after.value);
          i += 2;
          continue;
        }
        out.push(pad(depth) + tk.value);
        depth++;
        continue;
      }
      // text, self-closing tag, comment, CDATA, declaration, processing instruction
      out.push(pad(depth) + tk.value);
    }
    return out.join("\n");
  }

  function pad(depth) {
    var s = "";
    for (var i = 0; i < depth; i++) s += "  ";
    return s;
  }

  function tokenizeMarkup(s) {
    var tokens = [];
    var i = 0;
    var n = s.length;
    while (i < n) {
      if (s.charAt(i) === "<") {
        var end;
        if (s.substr(i, 4) === "<!--") {
          end = s.indexOf("-->", i + 4);
          end = end === -1 ? n : end + 3;
          tokens.push({ kind: "raw", value: s.slice(i, end) });
        } else if (s.substr(i, 9) === "<![CDATA[") {
          end = s.indexOf("]]>", i + 9);
          end = end === -1 ? n : end + 3;
          tokens.push({ kind: "raw", value: s.slice(i, end) });
        } else {
          end = s.indexOf(">", i);
          end = end === -1 ? n : end + 1;
          var tag = s.slice(i, end);
          tokens.push({ kind: classifyTag(tag), value: tag, name: tagName(tag) });
        }
        i = end;
      } else {
        var lt = s.indexOf("<", i);
        if (lt === -1) lt = n;
        var txt = s.slice(i, lt).trim();
        if (txt) tokens.push({ kind: "text", value: txt });
        i = lt;
      }
    }
    return tokens;
  }

  function classifyTag(tag) {
    if (tag.indexOf("</") === 0) return "close";
    if (tag.indexOf("<?") === 0 || tag.indexOf("<!") === 0) return "decl";
    if (/\/\s*>$/.test(tag)) return "self";
    return "open";
  }

  function tagName(tag) {
    var m = /^<\/?\s*([a-zA-Z0-9:_.-]+)/.exec(tag);
    return m ? m[1].toLowerCase() : "";
  }

  var api = { detect: detect, beautify: beautify };

  if (typeof module !== "undefined" && module.exports) {
    module.exports = api;
  } else {
    (typeof window !== "undefined" ? window : globalThis).__bodyformat = api;
  }
})();
