# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in CLIAnywhere Daemon (`claw`),
please report it responsibly:

- **Email:** [support@clianywhere.com](mailto:support@clianywhere.com)

Please **do not** open a public GitHub issue for security-related reports.

When reporting, include as much of the following as possible:

- A description of the issue and its potential impact
- Steps to reproduce (proof of concept, logs, etc.)
- Affected versions / commit SHA
- Any suggested fix or mitigation

We will acknowledge receipt within **3 business days** and aim to send an
initial assessment within **14 days**. Coordinated disclosure timelines are
negotiable; please do not publish details until a fix has been released.

## Scope

This policy covers the code in this repository. Out of scope:

- Vulnerabilities in third-party dependencies (report them upstream)
- Issues requiring root/admin access to a machine the attacker already controls
- Self-inflicted misconfiguration (e.g., exposing ports to the public internet)

## Supported Versions

Only the latest release line receives security fixes. Please run a recent
build before reporting.
