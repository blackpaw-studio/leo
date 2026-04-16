# Security Policy

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub
issues, discussions, or pull requests.**

Instead, report them privately via GitHub's private vulnerability
reporting:

1. Go to the [Security tab](https://github.com/blackpaw-studio/leo/security)
   of this repository.
2. Click **Report a vulnerability**.
3. Fill out the form with as much detail as you can provide.

If that channel is not available to you, email **security@blackpaw.studio**
with the same information.

## What to include

Please include as much of the following as you can — it dramatically
speeds up triage:

- Type of issue (e.g., remote code execution, credential exposure,
  privilege escalation).
- Full paths of affected source file(s).
- Version / commit SHA where the issue was observed.
- Step-by-step reproduction, including any configuration required.
- Proof-of-concept or exploit code, if you have one.
- Impact assessment — what an attacker could do with this issue.

## What to expect

- **Acknowledgement** within 3 business days.
- **Initial assessment** within 7 business days, including a severity
  estimate and an expected timeline for a fix.
- **Coordinated disclosure**: we will work with you on a public
  disclosure timeline. By default we aim to publish a fix and advisory
  within 90 days of the original report.

You will be credited in the advisory unless you request otherwise.

## Supported versions

Only the latest minor release receives security fixes. Older versions
should upgrade to the latest release.

## Scope

In-scope:

- The `leo` binary and its IPC surfaces (Unix socket, web UI, MCP server).
- The release and update pipeline (GoReleaser, install script).

Out of scope:

- Vulnerabilities in third-party Claude Code plugins (report those to
  the plugin authors).
- Vulnerabilities in the upstream `claude` CLI (report those to Anthropic).
- Issues that require physical access to an already-compromised machine.
