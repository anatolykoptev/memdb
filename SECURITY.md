# Security Policy

## Supported Versions

During the 0.x phase, only the latest 0.x minor release receives security fixes.
We will commit to an LTS policy once v1.0 ships.

| Version | Supported |
|---------|-----------|
| 0.x (latest minor) | Yes |
| older 0.x minors | No — upgrade to latest |
| v1.x / v2.x (pre-public internal tags) | No — superseded by v0.22.0 |

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Email: **piteronlinetv@gmail.com**

Please use the subject line: `[MemDB Security] <brief description>`

### What to include

- Description of the vulnerability
- Steps to reproduce
- Potential impact assessment
- Any suggested remediation (optional)

### Response SLA

- **72 hours**: acknowledgment of your report
- **14 days**: initial triage and severity assessment
- **90 days**: coordinated disclosure window — we will work with you to
  release a fix before public disclosure

### Disclosure Policy

We follow [coordinated disclosure](https://en.wikipedia.org/wiki/Coordinated_vulnerability_disclosure):

1. Reporter submits privately.
2. We confirm, triage, and develop a fix within 90 days.
3. We release the fix and publish a security advisory.
4. Reporter may publish after the advisory is public.

If a fix requires longer than 90 days, we will negotiate an extension with
the reporter.

## Known Security Considerations

### Authentication

MemDB uses Bearer token auth (`Authorization: Bearer <MASTER_KEY>`) and an
optional `X-Service-Secret` header for internal service-to-service calls.
Keep `MASTER_KEY` secret — it grants full access to all namespaces.

### User isolation

Queries are scoped by `user_id` in SQL predicates. This is a **logical
isolation** boundary, not a sandbox. MemDB is designed for single-tenant or
trusted-multi-user deployments where all callers share the same secret.
Do not expose MemDB to untrusted users without an additional auth layer.

### LLM API traffic

When MemDB calls an LLM (e.g. for re-ranking or summarization), requests go
through the configured `LLM_API_BASE` URL. Make sure this endpoint is trusted
and that `LLM_API_KEY` is rotated regularly. Traffic is not end-to-end
encrypted beyond what your TLS termination provides.

### Network exposure

The default `docker compose` setup binds MemDB ports to `0.0.0.0`. In
production, put a reverse proxy (nginx, Caddy) in front and restrict direct
port access with firewall rules.

## Bug Bounty

There is no formal bug bounty program. We credit reporters by name
(or handle) in release notes unless they prefer to remain anonymous.
