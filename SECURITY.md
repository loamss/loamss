# Security Policy

## Supported versions

Loamss is **pre-1.0**. Only the `main` branch is supported; older commits are not maintained. Once 1.0 ships, this section will be updated with a real version-support matrix.

## Reporting a vulnerability

**Please do not file public issues for security vulnerabilities.**

If you've found a security issue in Loamss — in the runtime, the specs, a reference capsule, or a reference adapter — report it through one of these channels:

1. **GitHub Security Advisory (preferred)**: open a private advisory at [github.com/loamss/loamss/security/advisories/new](https://github.com/loamss/loamss/security/advisories/new). This is the most direct channel; it stays private until disclosure.
2. **Email**: `security@loamss.com`.

Include:

- A description of the issue
- Steps to reproduce, if applicable
- Affected components (which spec, which adapter, which capsule version)
- Your assessment of severity and exploitability
- Whether you'd like credit in any eventual public advisory

## What to expect

| Step | Expected timing |
|---|---|
| Acknowledgment of your report | Within 72 hours |
| Initial assessment + severity classification | Within 1 week |
| Fix or mitigation in `main` | Depends on severity; critical issues prioritized |
| Coordinated disclosure | Discussed with you; typically 30-90 days after fix lands |

We will keep you informed throughout. If you don't hear back within 72 hours, please ping the email channel — security reports occasionally get caught in filters.

## Scope

In scope:

- The Loamss runtime (when it exists)
- The capsule specification, MCP surface, permission model, adapter interface, audit log schema — design-level vulnerabilities
- Reference capsules and adapters shipped from the canonical repo
- The capsule registry (when it exists)
- The console (when it exists)

Out of scope:

- Third-party capsules in the registry (report to their authors)
- Third-party adapters not shipped from this repo (same)
- Vulnerabilities in upstream MCP — report to the [MCP project](https://modelcontextprotocol.io)
- Vulnerabilities in user-configured backends (S3, Postgres, etc.) — report to those vendors

## Disclosure policy

We follow **coordinated disclosure**. We will not publish a vulnerability until a fix is available and users have had reasonable opportunity to upgrade. We will credit researchers who report responsibly, unless they prefer to remain anonymous.

We do **not** currently operate a bug bounty program. We may in the future. If you're reporting in good faith, you have our gratitude regardless of monetary compensation.

## Hall of fame

Researchers who have reported security issues will be acknowledged here (with their consent) once the first valid report lands.

— pending —

## Threat model expectations

Loamss is designed around specific threat models documented in [`ARCHITECTURE.md`](ARCHITECTURE.md) (the trust tiers) and [`audit-spec.md`](audit-spec.md) (the tamper-evidence chain). Reports that demonstrate violations of those models — for example, a capsule bypassing the permission framework, an external client reading data outside its granted scope, or an audit log entry being tampered without detection — are the highest-severity class.

Reports that demonstrate weaknesses beyond the documented threat model (e.g., a malicious storage adapter exfiltrating data — but adapters are explicitly semi-trusted) are still welcome and may inform future model evolution, but won't be treated as primary security vulnerabilities.

Read the threat model before reporting; this saves time on both sides.
