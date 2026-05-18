package admin

// loginHTML is the raw HTML source for the admin login page. Issue 05 will
// replace this constant with an embed.FS read so the template lives in a
// separate file.
const loginHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>httpcatch — log in</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: system-ui, -apple-system, sans-serif;
    background: #f4f4f5;
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    color: #18181b;
  }
  .card {
    background: #fff;
    border-radius: 8px;
    box-shadow: 0 1px 4px rgba(0,0,0,.12);
    padding: 2rem;
    width: 100%;
    max-width: 360px;
  }
  h1 { font-size: 1.25rem; margin-bottom: 1.5rem; }
  label { display: block; font-size: .875rem; margin-bottom: .375rem; color: #52525b; }
  input[type=password] {
    width: 100%;
    padding: .5rem .75rem;
    border: 1px solid #d4d4d8;
    border-radius: 6px;
    font-size: 1rem;
    margin-bottom: 1rem;
  }
  button {
    width: 100%;
    padding: .5rem;
    background: #2563eb;
    color: #fff;
    border: none;
    border-radius: 6px;
    font-size: 1rem;
    cursor: pointer;
  }
  button:hover { background: #1d4ed8; }
  .error {
    background: #fef2f2;
    border: 1px solid #fca5a5;
    border-radius: 6px;
    padding: .5rem .75rem;
    font-size: .875rem;
    color: #b91c1c;
    margin-bottom: 1rem;
  }
</style>
</head>
<body>
<div class="card">
  <h1>httpcatch</h1>
  {{if .Error}}<div class="error">Invalid token. Please try again.</div>{{end}}
  <form method="POST" action="/auth/login">
    {{if .Next}}<input type="hidden" name="next" value="{{.Next}}">{{end}}
    <label for="token">Admin token</label>
    <input type="password" id="token" name="token" autocomplete="current-password" autofocus>
    <button type="submit">Log in</button>
  </form>
</div>
</body>
</html>
`
