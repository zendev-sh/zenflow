# Security Policy

## Supported versions

zenflow is pre-1.0. The latest released version on the `main` branch is supported with security fixes.

| Version | Supported |
|---------|-----------|
| `0.x.x` | Yes (latest) |
| `< 0.x` | No |

## Reporting a vulnerability

Please report security vulnerabilities privately to `security@zendev.sh`.

Include:

- A description of the issue
- Steps to reproduce (or a proof-of-concept)
- The version / commit affected
- Any suggested mitigations

You should receive an acknowledgement within 48 hours. We aim to publish a fix within 30 days; complex issues may take longer and we will keep you informed.

## Scope

In scope:

- The `zenflow` library and CLI binary
- The `spec/v1/` workflow specification
- The OSS docsite at https://zenflow.sh

Out of scope:

- Vulnerabilities in third-party dependencies (report those upstream)
- Vulnerabilities in user-authored workflows or tool implementations
- Social engineering of maintainers or contributors

## Disclosure

We follow a coordinated disclosure model. We ask that you do not publicly disclose the issue until a fix is released and a reasonable upgrade window has passed (typically 30 days).

We will credit the reporter in the release notes unless anonymity is requested.
