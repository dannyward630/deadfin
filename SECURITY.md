# Security policy

## Report a vulnerability

Report security problems privately through GitHub private vulnerability
reporting:

<https://github.com/deadfin-org/deadfin/security/advisories/new>

The report is private to the maintainers. Include:

- The Deadfin build: the output of
  `curl -s http://<host>:8096/deadfin/v1/build`.
- What an attacker can do, and what they need first (network position, an
  account, a role).
- Steps or a request sequence that shows the problem.
- Placeholders in place of real tokens, passwords, and API keys.

We will acknowledge the report within 7 days. We will tell you when a fix is
ready and credit you in the advisory if you want.

## Supported versions

Deadfin is alpha software. Security fixes go to the newest release tag.

## Scope

This policy covers the Deadfin server in this repository and the release
images at `ghcr.io/deadfin-org/deadfin`. Report problems in Jellyfin clients,
jellyfin-web, or the third-party programs in the image to their upstream
projects.

## Hardening notes

- Finish the first-run wizard before you expose a new server to the internet.
  See [docs/alpha/install.md](docs/alpha/install.md#first-run-setup-and-the-setup-link).
- Bind `DEADFIN_PPROF_ADDR` to a loopback address, if you set it.
