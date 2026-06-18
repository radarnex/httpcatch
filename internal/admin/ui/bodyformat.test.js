// Table-driven test runner for bodyformat.js, mirroring searchql.test.js.
// Run with: `node internal/admin/ui/bodyformat.test.js`.
//
// Exits non-zero on any failure so CI / pre-commit hooks can wire it up.

var bf = require("./bodyformat.js");

var failed = 0;
var passed = 0;
var current = "";

function record(ok, message) {
  if (ok) { passed++; return; }
  failed++;
  console.error("FAIL " + current + (message ? "\n  " + message : ""));
}

function eq(label, got, want) {
  current = label;
  if (got === want) { record(true); return; }
  record(false, "got:  " + JSON.stringify(got) + "\nwant: " + JSON.stringify(want));
}

// ── detect: Content-Type is authoritative ────────────────────────
eq("json by content-type", bf.detect("application/json", ""), "json");
eq("json with charset param", bf.detect("application/json; charset=utf-8", ""), "json");
eq("json +json suffix", bf.detect("application/vnd.api+json", ""), "json");
eq("xml application/xml", bf.detect("application/xml", ""), "xml");
eq("xml text/xml with param", bf.detect("text/xml; charset=utf-8", ""), "xml");
eq("xml +xml suffix (svg)", bf.detect("image/svg+xml", ""), "xml");
eq("html text/html", bf.detect("text/html", ""), "html");
eq("form urlencoded", bf.detect("application/x-www-form-urlencoded", "a=b"), "form");

// ── detect: sniffing when Content-Type is absent or generic ───────
eq("sniff json object", bf.detect("", "  {\"a\":1}"), "json");
eq("sniff json array", bf.detect("application/octet-stream", "[1,2]"), "json");
eq("sniff xml", bf.detect("", "<root/>"), "xml");
eq("sniff html doctype", bf.detect("text/plain", "<!DOCTYPE html><html></html>"), "html");
eq("sniff html tag", bf.detect("text/plain", "<html><body></body></html>"), "html");
eq("form is never sniffed", bf.detect("", "a=b&c=d"), "");
eq("plain prose is not beautifiable", bf.detect("text/plain", "hello world"), "");
eq("unknown content-type, no sniff match", bf.detect("application/pdf", "%PDF-1.4"), "");

// ── beautify: JSON ───────────────────────────────────────────────
eq("json pretty-prints nested", bf.beautify('{"a":1,"b":[1,2]}', "json"),
  '{\n  "a": 1,\n  "b": [\n    1,\n    2\n  ]\n}');
eq("json malformed returns null", bf.beautify("{bad json", "json"), null);
eq("empty body returns null", bf.beautify("", "json"), null);
eq("null body returns null", bf.beautify(null, "json"), null);

// ── beautify: form-urlencoded ─────────────────────────────────────
eq("form one pair per line", bf.beautify("username=bob&password=hunter2&remember=true", "form"),
  "username = bob\npassword = hunter2\nremember = true");
eq("form percent- and plus-decoding", bf.beautify("a=hello%20world&b=x+y", "form"),
  "a = hello world\nb = x y");
eq("form value-less key", bf.beautify("flag", "form"), "flag = ");

// ── beautify: XML ─────────────────────────────────────────────────
eq("xml soap envelope indents, inlines text-only elements",
  bf.beautify(
    '<?xml version="1.0"?><soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">' +
    '<soap:Body><GetUser><UserId>42</UserId></GetUser></soap:Body></soap:Envelope>', "xml"),
  '<?xml version="1.0"?>\n' +
  '<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">\n' +
  '  <soap:Body>\n' +
  '    <GetUser>\n' +
  '      <UserId>42</UserId>\n' +
  '    </GetUser>\n' +
  '  </soap:Body>\n' +
  '</soap:Envelope>');
eq("xml self-closing tag keeps depth", bf.beautify("<root><item/></root>", "xml"),
  "<root>\n  <item/>\n</root>");
eq("xml with no tags returns null", bf.beautify("just text", "xml"), null);

// ── beautify: HTML void elements do not open a level ──────────────
eq("html void elements stay flat", bf.beautify('<div><br><img src="x"></div>', "html"),
  '<div>\n  <br>\n  <img src="x">\n</div>');

// ── Summary ──────────────────────────────────────────────────────
console.log("bodyformat.js: " + passed + " passed, " + failed + " failed");
if (failed > 0) process.exit(1);
