// JS mirror of internal/searchql/searchql.go. Exposes parseQuery, tokenize,
// and isUnindexedScan so the UI can render chips, toggle the unindexed-scan
// banner, and surface the OR-hint without a server round-trip. The shape of
// the returned AST matches the Go searchql.Query/Term fields case-for-case
// (field, value, wildcard, quoted, negated, headerName, statusFilter) so JS
// tests can mirror the Go parser tests.
(function () {
  var WILDCARD_NONE = 0;
  var WILDCARD_PREFIX = 1;
  var WILDCARD_SUBSTRING = 2;

  var FIELD_HOST = "host";
  var FIELD_PATH = "path";
  var FIELD_SERVICE = "service";
  var FIELD_BODY = "body";
  var FIELD_HEADERS = "headers";
  var FIELD_HEADER = "header";
  var FIELD_METHOD = "method";
  var FIELD_STATUS = "status";
  var FIELD_SOURCE_IP = "source_ip";
  var FIELD_CORRELATION_ID = "correlation_id";

  var KNOWN_FIELDS = {
    host: FIELD_HOST,
    path: FIELD_PATH,
    service: FIELD_SERVICE,
    body: FIELD_BODY,
    headers: FIELD_HEADERS,
    method: FIELD_METHOD,
    status: FIELD_STATUS,
    source_ip: FIELD_SOURCE_IP,
    correlation_id: FIELD_CORRELATION_ID,
  };

  var INDEXED_FIELDS = { host: true, path: true, service: true };
  var STRUCTURED_FIELDS = {
    method: true, status: true, source_ip: true, correlation_id: true,
  };
  var CANONICAL_METHODS = {
    GET: true, HEAD: true, POST: true, PUT: true, DELETE: true,
    CONNECT: true, OPTIONS: true, TRACE: true, PATCH: true,
  };

  function isSpace(c) {
    return c === " " || c === "\t" || c === "\n" || c === "\r" || c === "\v" || c === "\f";
  }

  function tokenize(s) {
    var tokens = [];
    var i = 0;
    var n = s.length;
    while (i < n) {
      while (i < n && isSpace(s[i])) i++;
      if (i >= n) break;
      var start = i;
      var inQuote = false;
      while (i < n) {
        var c = s[i];
        if (inQuote) {
          if (c === "\\" && i + 1 < n && s[i + 1] === "\"") { i += 2; continue; }
          if (c === "\"") { inQuote = false; i++; continue; }
          i++;
          continue;
        }
        if (c === "\"") { inQuote = true; i++; continue; }
        if (isSpace(c)) break;
        i++;
      }
      if (inQuote) {
        return { error: { token: s.slice(start), message: "unclosed quote" } };
      }
      tokens.push(s.slice(start, i));
    }
    return { tokens: tokens };
  }

  function canonicalHeaderName(name) {
    var out = "";
    var upper = true;
    for (var i = 0; i < name.length; i++) {
      var c = name.charCodeAt(i);
      if (upper) {
        if (c >= 97 && c <= 122) c -= 32;
        upper = false;
      } else if (c >= 65 && c <= 90) {
        c += 32;
      }
      var ch = String.fromCharCode(c);
      out += ch;
      if (ch === "-") upper = true;
    }
    return out;
  }

  function parseValue(valueRaw) {
    if (valueRaw.length > 0 && valueRaw.charAt(0) === "\"") {
      if (valueRaw.length < 2 || valueRaw.charAt(valueRaw.length - 1) !== "\"") {
        return { error: "unclosed quote" };
      }
      var inner = valueRaw.slice(1, -1);
      return { value: inner.replace(/\\"/g, "\""), wildcard: WILDCARD_NONE, quoted: true };
    }
    var v = valueRaw;
    var leading = v.length > 0 && v.charAt(0) === "*";
    if (leading) v = v.slice(1);
    var trailing = v.length > 0 && v.charAt(v.length - 1) === "*";
    if (trailing) v = v.slice(0, -1);
    if (v === "") {
      return { error: "value must contain a literal between wildcards" };
    }
    if (v.indexOf("*") !== -1) {
      return { error: "wildcards '*' are only allowed at the start or end of a value" };
    }
    var wildcard = WILDCARD_NONE;
    if (leading) wildcard = WILDCARD_SUBSTRING;
    else if (trailing) wildcard = WILDCARD_PREFIX;
    return { value: v, wildcard: wildcard, quoted: false };
  }

  function parseStatusValue(s) {
    if (s.length === 3 && s.charAt(1) === "x" && s.charAt(2) === "x") {
      var d = s.charCodeAt(0);
      if (d >= 49 && d <= 53) {
        return { class: s };
      }
    }
    if (!/^\d+$/.test(s)) {
      return { error: "must be an integer status code (e.g. 200) or class form (e.g. 2xx, 5xx)" };
    }
    var n = parseInt(s, 10);
    if (n < 100 || n > 599) {
      return { error: "must be an integer status code (e.g. 200) or class form (e.g. 2xx, 5xx)" };
    }
    return { exact: n };
  }

  function emptyTerm() {
    return {
      field: "",
      value: "",
      wildcard: WILDCARD_NONE,
      quoted: false,
      negated: false,
      headerName: "",
    };
  }

  function parseToken(raw) {
    var tok = raw;
    var negated = false;
    if (tok.length > 0 && tok.charAt(0) === "-") {
      negated = true;
      tok = tok.slice(1);
      if (tok === "") {
        return { error: { token: raw, message: "bare '-' is not a valid token" } };
      }
    }

    if (tok.length > 0 && tok.charAt(0) === "\"") {
      var pv = parseValue(tok);
      if (pv.error) return { error: { token: raw, message: pv.error } };
      var t = emptyTerm();
      t.value = pv.value; t.wildcard = pv.wildcard; t.quoted = pv.quoted; t.negated = negated;
      return { term: t };
    }

    var idx = tok.indexOf(":");
    if (idx < 0) {
      var pv2 = parseValue(tok);
      if (pv2.error) return { error: { token: raw, message: pv2.error } };
      var t2 = emptyTerm();
      t2.value = pv2.value; t2.wildcard = pv2.wildcard; t2.quoted = pv2.quoted; t2.negated = negated;
      return { term: t2 };
    }
    if (idx === 0) {
      return { error: { token: raw, message: "token must start with a key before ':'" } };
    }
    var key = tok.slice(0, idx);
    var valueRaw = tok.slice(idx + 1);
    if (valueRaw === "") {
      return { error: { token: raw, message: "empty value after ':'" } };
    }
    var keyLow = key.toLowerCase();
    if (!KNOWN_FIELDS[keyLow] && keyLow.indexOf("header.") === 0) {
      var name = key.slice("header.".length);
      if (name === "") {
        return { error: { token: raw, message: "empty header name after 'header.'" } };
      }
      if (name.indexOf("*") !== -1) {
        return { error: { token: raw, message: "wildcards are not supported in header name; use 'headers:' for fuzzy header search" } };
      }
      var pv3 = parseValue(valueRaw);
      if (pv3.error) return { error: { token: raw, message: pv3.error } };
      var t3 = emptyTerm();
      t3.field = FIELD_HEADER;
      t3.headerName = canonicalHeaderName(name);
      t3.value = pv3.value; t3.wildcard = pv3.wildcard; t3.quoted = pv3.quoted; t3.negated = negated;
      return { term: t3 };
    }
    var field = KNOWN_FIELDS[keyLow];
    if (!field) {
      return { error: { token: raw, message: "unknown key \"" + key + "\"" } };
    }
    var pv4 = parseValue(valueRaw);
    if (pv4.error) return { error: { token: raw, message: pv4.error } };
    if (STRUCTURED_FIELDS[field] && pv4.wildcard !== WILDCARD_NONE) {
      return { error: { token: raw, message: "wildcards are not supported on field \"" + key + "\"" } };
    }
    var term = emptyTerm();
    term.field = field;
    term.value = pv4.value;
    term.wildcard = pv4.wildcard;
    term.quoted = pv4.quoted;
    term.negated = negated;
    if (field === FIELD_METHOD) {
      var up = pv4.value.toUpperCase();
      if (!CANONICAL_METHODS[up]) {
        return { error: { token: raw, message: "unknown HTTP method \"" + pv4.value + "\"" } };
      }
      term.value = up;
    } else if (field === FIELD_STATUS) {
      var sf = parseStatusValue(pv4.value);
      if (sf.error) return { error: { token: raw, message: sf.error } };
      term.statusFilter = sf;
    }
    return { term: term };
  }

  function parseQuery(s) {
    var tk = tokenize(s);
    if (tk.error) return { terms: [], error: tk.error };
    var terms = [];
    for (var i = 0; i < tk.tokens.length; i++) {
      var pt = parseToken(tk.tokens[i]);
      if (pt.error) return { terms: [], error: pt.error };
      terms.push(pt.term);
    }
    return { terms: terms };
  }

  function isUnindexedScan(q) {
    if (!q || !q.terms) return false;
    for (var i = 0; i < q.terms.length; i++) {
      var t = q.terms[i];
      if (t.wildcard !== WILDCARD_SUBSTRING) continue;
      if (t.field === "" || INDEXED_FIELDS[t.field]) return true;
    }
    return false;
  }

  // chipDisplayValue mirrors the Go server's chipDisplayValue: status terms
  // use the original Exact/Class text; quoted values are wrapped in `"…"`
  // with `"` re-escaped; wildcarded values prepend or append `*`; bare values
  // pass through.
  function chipDisplayValue(term) {
    if (term.statusFilter) {
      if (term.statusFilter.exact) return String(term.statusFilter.exact);
      return term.statusFilter.class;
    }
    if (term.quoted) {
      return "\"" + String(term.value).replace(/"/g, "\\\"") + "\"";
    }
    if (term.wildcard === WILDCARD_PREFIX) return term.value + "*";
    if (term.wildcard === WILDCARD_SUBSTRING) return "*" + term.value + "*";
    return term.value;
  }

  function chipFromTerm(term) {
    var value = chipDisplayValue(term);
    var key = term.field;
    if (term.field === FIELD_HEADER) key = "header." + term.headerName;
    var freeform = term.field === "";
    var token = freeform ? value : key + ":" + value;
    if (term.negated) token = "-" + token;
    return {
      token: token,
      key: key,
      value: value,
      negated: !!term.negated,
      isHeader: term.field === FIELD_HEADERS || term.field === FIELD_HEADER,
      isFreeform: freeform,
    };
  }

  // shouldShowOrHint detects the literal `OR` between two field-qualified
  // terms — the three-consecutive-terms pattern from slice 06. Returns the
  // matched 0-based index of the middle `OR` token (so callers can highlight)
  // or -1 when the pattern is absent.
  function shouldShowOrHint(q) {
    if (!q || !q.terms || q.terms.length < 3) return -1;
    for (var i = 1; i < q.terms.length - 1; i++) {
      var mid = q.terms[i];
      if (mid.field !== "" || mid.value !== "OR" || mid.negated || mid.quoted) continue;
      if (mid.wildcard !== WILDCARD_NONE) continue;
      var prev = q.terms[i - 1];
      var next = q.terms[i + 1];
      if (prev.field !== "" && next.field !== "") return i;
    }
    return -1;
  }

  var api = {
    parseQuery: parseQuery,
    tokenize: tokenize,
    isUnindexedScan: isUnindexedScan,
    chipDisplayValue: chipDisplayValue,
    chipFromTerm: chipFromTerm,
    shouldShowOrHint: shouldShowOrHint,
    canonicalHeaderName: canonicalHeaderName,
    WildcardNone: WILDCARD_NONE,
    WildcardPrefix: WILDCARD_PREFIX,
    WildcardSubstring: WILDCARD_SUBSTRING,
    FieldHeader: FIELD_HEADER,
    FieldHeaders: FIELD_HEADERS,
  };

  if (typeof module !== "undefined" && module.exports) {
    module.exports = api;
  } else {
    (typeof window !== "undefined" ? window : globalThis).__searchql = api;
  }
})();
