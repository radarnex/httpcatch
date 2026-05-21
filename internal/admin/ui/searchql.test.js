// Minimal table-driven test runner for searchql.js. Mirrors a subset of the
// Go parser tests so divergence between the JS and Go grammars surfaces
// quickly. Run with: `node internal/admin/ui/searchql.test.js`.
//
// Exits non-zero on any failure so CI / pre-commit hooks can wire it up.

var sql = require("./searchql.js");

var failed = 0;
var passed = 0;
var current = "";

function record(ok, message) {
  if (ok) { passed++; return; }
  failed++;
  console.error("FAIL " + current + (message ? "\n  " + message : ""));
}

function deepEqual(a, b) {
  if (a === b) return true;
  if (typeof a !== typeof b) return false;
  if (a && b && typeof a === "object") {
    var ka = Object.keys(a);
    var kb = Object.keys(b);
    if (ka.length !== kb.length) return false;
    for (var i = 0; i < ka.length; i++) {
      if (!deepEqual(a[ka[i]], b[ka[i]])) return false;
    }
    return true;
  }
  return false;
}

function eq(label, got, want) {
  current = label;
  if (deepEqual(got, want)) { record(true); return; }
  record(false, "got:  " + JSON.stringify(got) + "\nwant: " + JSON.stringify(want));
}

function assert(label, cond) {
  current = label;
  record(!!cond, cond ? "" : "condition false");
}

// ── parseQuery: empty ────────────────────────────────────────────
eq("empty string yields no terms", sql.parseQuery("").terms, []);
eq("whitespace yields no terms", sql.parseQuery("   \t  ").terms, []);

// ── parseQuery: freeform ─────────────────────────────────────────
eq("freeform bare", sql.parseQuery("billing-api").terms, [{
  field: "", value: "billing-api", wildcard: 0, quoted: false, negated: false, headerName: "",
}]);
eq("freeform trailing wildcard", sql.parseQuery("billing-api*").terms, [{
  field: "", value: "billing-api", wildcard: 1, quoted: false, negated: false, headerName: "",
}]);
eq("freeform leading wildcard (substring)", sql.parseQuery("*billing").terms, [{
  field: "", value: "billing", wildcard: 2, quoted: false, negated: false, headerName: "",
}]);
eq("freeform both-sides wildcard (substring)", sql.parseQuery("*billing*").terms, [{
  field: "", value: "billing", wildcard: 2, quoted: false, negated: false, headerName: "",
}]);
eq("freeform quoted", sql.parseQuery('"hello world"').terms, [{
  field: "", value: "hello world", wildcard: 0, quoted: true, negated: false, headerName: "",
}]);
eq("freeform negated", sql.parseQuery("-billing").terms, [{
  field: "", value: "billing", wildcard: 0, quoted: false, negated: true, headerName: "",
}]);

// ── parseQuery: field-qualified ──────────────────────────────────
eq("service exact", sql.parseQuery("service:orders").terms, [{
  field: "service", value: "orders", wildcard: 0, quoted: false, negated: false, headerName: "",
}]);
eq("host trailing wildcard", sql.parseQuery("host:billing-api*").terms, [{
  field: "host", value: "billing-api", wildcard: 1, quoted: false, negated: false, headerName: "",
}]);
eq("path substring wildcard", sql.parseQuery("path:*signup*").terms, [{
  field: "path", value: "signup", wildcard: 2, quoted: false, negated: false, headerName: "",
}]);
eq("body quoted with literal star", sql.parseQuery('body:"*foo*"').terms, [{
  field: "body", value: "*foo*", wildcard: 0, quoted: true, negated: false, headerName: "",
}]);
eq("negated host substring", sql.parseQuery("-host:*api*").terms, [{
  field: "host", value: "api", wildcard: 2, quoted: false, negated: true, headerName: "",
}]);

// ── parseQuery: per-header ───────────────────────────────────────
var hdr = sql.parseQuery("header.user-agent:client/0.3");
eq("per-header term", hdr.terms[0], {
  field: "header", value: "client/0.3", wildcard: 0, quoted: false, negated: false,
  headerName: "User-Agent",
});
eq("per-header term canonicalises mixed case", sql.parseQuery("header.USER-AGENT:foo").terms[0].headerName, "User-Agent");
eq("per-header term canonicalises x-trace-id", sql.parseQuery("header.x-trace-id:abc").terms[0].headerName, "X-Trace-Id");

// ── parseQuery: structured field validation ──────────────────────
var bad = sql.parseQuery("method:NOTAVERB");
assert("unknown method is parse error", bad.error && bad.error.message.indexOf("unknown HTTP method") >= 0);
var statusExact = sql.parseQuery("status:200").terms[0];
eq("status exact filter", statusExact.statusFilter, { exact: 200 });
var statusClass = sql.parseQuery("status:5xx").terms[0];
eq("status class filter", statusClass.statusFilter, { class: "5xx" });

// ── parseQuery: errors ───────────────────────────────────────────
var unclosed = sql.parseQuery('host:"unterminated');
assert("unclosed quote produces error", unclosed.error && unclosed.error.message.indexOf("unclosed") >= 0);
var unknownKey = sql.parseQuery("foo:bar");
assert("unknown key produces error", unknownKey.error && unknownKey.error.message.indexOf("unknown key") >= 0);
var headerWild = sql.parseQuery("header.*-agent:foo");
assert("wildcard in header name is rejected", headerWild.error && headerWild.error.message.indexOf("wildcards are not supported in header name") >= 0);

// ── isUnindexedScan: case table from the design grilling ─────────
var scanCases = [
  ["", false],
  ["service:orders", false],
  ["host:billing-api", false],
  ["host:billing-api*", false],
  ["path:/api/*", false],
  ["body:foo", false],
  ["body:*foo*", false],
  ["host:*api*", true],
  ["host:*api", true],
  ["path:*signup", true],
  ["service:*foo*", true],
  ["service:orders host:*api*", true],
  ["service:orders -host:*api*", true],
  ["billing-api", false],
  ["billing-api*", false],
  ["*billing*", true],
  ["*billing", true],
  ["service:orders *billing*", true],
  ["headers:foo", false],
  ["header.user-agent:client", false],
];
for (var i = 0; i < scanCases.length; i++) {
  var input = scanCases[i][0];
  var want = scanCases[i][1];
  var got = sql.isUnindexedScan(sql.parseQuery(input));
  eq("isUnindexedScan(" + JSON.stringify(input) + ")", got, want);
}

// ── shouldShowOrHint: three-consecutive-terms pattern ────────────
assert("shows hint for service:foo OR service:bar",
  sql.shouldShowOrHint(sql.parseQuery("service:foo OR service:bar")) === 1);
assert("shows hint when one side is per-header",
  sql.shouldShowOrHint(sql.parseQuery("service:foo OR header.user-agent:bar")) === 1);
assert("no hint when middle is quoted OR",
  sql.shouldShowOrHint(sql.parseQuery('service:foo "OR" service:bar')) === -1);
assert("no hint when OR has wildcards",
  sql.shouldShowOrHint(sql.parseQuery("service:foo OR* service:bar")) === -1);
assert("no hint for body:OR (single term containing OR)",
  sql.shouldShowOrHint(sql.parseQuery("body:OR")) === -1);
assert("no hint when OR is the first token",
  sql.shouldShowOrHint(sql.parseQuery("OR service:foo")) === -1);
assert("no hint when OR is the last token",
  sql.shouldShowOrHint(sql.parseQuery("service:foo OR")) === -1);
assert("no hint for freeform OR between freeform terms",
  sql.shouldShowOrHint(sql.parseQuery("foo OR bar")) === -1);
assert("no hint for lowercase or",
  sql.shouldShowOrHint(sql.parseQuery("service:foo or service:bar")) === -1);

// ── chipFromTerm: chip rendering parity with the Go server ───────
var chips = sql.parseQuery("service:foo -host:*api* headers:trace header.user-agent:client billing-api").terms.map(sql.chipFromTerm);
eq("chip[0] service:foo", chips[0], {
  token: "service:foo", key: "service", value: "foo", negated: false, isHeader: false, isFreeform: false,
});
eq("chip[1] negated host substring", chips[1], {
  token: "-host:*api*", key: "host", value: "*api*", negated: true, isHeader: false, isFreeform: false,
});
eq("chip[2] headers keyword", chips[2], {
  token: "headers:trace", key: "headers", value: "trace", negated: false, isHeader: true, isFreeform: false,
});
eq("chip[3] per-header canonical name", chips[3], {
  token: "header.User-Agent:client", key: "header.User-Agent", value: "client", negated: false, isHeader: true, isFreeform: false,
});
eq("chip[4] freeform", chips[4], {
  token: "billing-api", key: "", value: "billing-api", negated: false, isHeader: false, isFreeform: true,
});

// ── tokenize: quote-aware splitting ──────────────────────────────
eq("tokenize freeform + quoted", sql.tokenize('foo "bar baz" qux').tokens, ['foo', '"bar baz"', 'qux']);
eq("tokenize escaped quote inside value", sql.tokenize('body:"with \\"quote\\" inside"').tokens, ['body:"with \\"quote\\" inside"']);

// ── Summary ──────────────────────────────────────────────────────
console.log("searchql.js: " + passed + " passed, " + failed + " failed");
if (failed > 0) process.exit(1);
