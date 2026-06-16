# Security Policy

## Supported versions

httpcatch is pre-1.0 and ships from a single release line. Security fixes land
on the latest released version. Always run the most recent release before
reporting an issue.

## Reporting a vulnerability

Please report security vulnerabilities **privately**. Do not open a public
issue for anything that could be exploited.

- Open a private security advisory via GitHub:
  [**Report a vulnerability**](https://github.com/radarnex/httpcatch/security/advisories/new).
- For low-severity issues with no exploit potential, you may open a regular
  issue instead — use your judgement.

When reporting, please include:

- The affected version (`httpcatch --version` or the image tag).
- A description of the issue and its impact.
- Reproduction steps or a proof of concept where possible.

You can expect an initial acknowledgement within a few days. Once a fix is
ready we will coordinate a release and credit you in the advisory unless you
prefer to remain anonymous.

## Scope and hardening notes

httpcatch captures and stores raw HTTP traffic, so its security posture is
documented in detail in the [threat model](docs/THREAT_MODEL.md). A few points
worth highlighting:

- **Captured data is sensitive.** Records can contain credentials, tokens, and
  PII. Configure [redaction](examples/redact.yaml) before pointing real traffic
  at the capture port. Running without redaction is supported but emits a
  startup warning and a persistent UI banner.
- **The admin surface is authenticated.** The inspect API, events API, and
  embedded UI require the admin token. The admin port refuses to bind a
  non-loopback address without a token (or an explicit insecure-mode opt-in).
- **The capture port is unauthenticated by design.** It accepts any HTTP
  request as captured traffic. Keep it on a private network and only expose it
  to your proxy/mirror, never the public internet.
