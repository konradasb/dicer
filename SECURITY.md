# Security Policy

## Supported Versions

Only the latest release of Dicer receives security fixes. We do not backport security patches to older versions.

| Version | Supported |
|---------|-----------|
| Latest  | Yes       |
| Older   | No        |

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

If you believe you have found a security vulnerability in Dicer, please report it privately using one of the following methods:

- **GitHub Security Advisories**: [Report a vulnerability](https://github.com/dicer-sh/dicer/security/advisories/new) (preferred)
- **Email**: Contact a maintainer listed in [MAINTAINERS.md](MAINTAINERS.md) directly

Please include as much of the following information as possible to help us understand and reproduce the issue:

- Type of issue (e.g. privilege escalation, arbitrary code execution, information disclosure)
- The component or file(s) involved
- Steps to reproduce the issue
- Proof-of-concept or exploit code (if available)
- Impact assessment — what an attacker could achieve

## Response Process

1. We will acknowledge receipt of your report within **3 business days**
2. We will investigate and provide an initial assessment within **7 business days**
3. We will work with you to understand the severity and develop a fix
4. We will coordinate a disclosure timeline — typically **90 days** from the report date, or sooner if a fix is available
5. We will credit reporters in the release notes unless they prefer to remain anonymous

## Scope

The following are in scope for security reports:

- `dicer-init` (PID 1 init binary) — privilege boundaries, escape paths from the VM
- `dicer-agent` (guest agent) — unauthorized command execution, vsock exposure
- VM isolation — host/guest boundary violations
- Cryptographic weaknesses in image verification or transport
- Dependency vulnerabilities with a direct, exploitable path in Dicer

Out of scope:

- Vulnerabilities in the host kernel or hypervisor (Cloud Hypervisor, KVM) — report these upstream
- Denial of service attacks requiring physical or privileged access to the host
- Issues in third-party dependencies with no exploitable path in this project

## Security Considerations for Users

Dicer runs virtual machines and requires elevated host privileges. Operators should:

- Run Dicer on a hardened Linux host with up-to-date kernel
- Restrict access to the daemon's Unix socket: anyone who can open it can run VMs,
  and through them read any file on the host, so it is as good as root
- If the API listens on TCP, treat enrolment tokens as secrets until used, keep
  their lifetime short, and remove clients that no longer need access with
  `dicer client delete`. Every trusted client has full control of the host's VMs,
  and can read any file on the host by injecting it into one (`--file`), since
  the daemon reads it as root: trust a client as you would root on the host
- Protect the daemon's data directory: it holds the daemon's private key and
  the list of trusted clients. Back up `tls/` in it: losing the key locks out
  every enrolled client until each re-enrols
- If the daemon's key may be compromised, replace it and re-enrol every client
  from fresh tokens; if a client's may be, run `dicer client delete` for it on
  every daemon. See "Keys" in the README for the steps, and why a daemon key
  is replaced rather than rotated
- Treat any untrusted VM workload as potentially hostile to the host boundary
- Keep Dicer updated to the latest release

## Disclosure Policy

We follow a coordinated disclosure model. We ask that reporters give us reasonable time to develop and release a fix before public disclosure. We will always credit reporters (with their consent) in our security advisories.
