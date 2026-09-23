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

- **GitHub Security Advisories**: [Report a vulnerability](https://github.com/konradasb/dicer/security/advisories/new) (preferred)
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

- VM isolation — a guest reaching the host, or another guest, through anything
  Dicer sets up: its disks, its vsock channel, its network
- `dicer-init` (the guest's PID 1) and `dicer-agent` (the guest agent) —
  privilege boundaries, command execution the host did not ask for, vsock
  exposure
- The daemon's API — its TLS and client certificate handling, and any way to
  use it without being allowed to
- Host networking — traffic crossing between networks, or between instances
  on an `--isolated` network, that the rules should stop
- Files crossing the boundary — `dicer cp` writing outside the path it was
  given on either side, and `file` mounts reading more than the file named
- Integrity — images not matching their digest, or a kernel not matching the
  `--sha256` it was imported with
- Dependency vulnerabilities with a direct, exploitable path in Dicer

Out of scope:

- Vulnerabilities in the host kernel or the hypervisors (KVM, Cloud Hypervisor,
  Firecracker) — report these upstream
- What follows from having the daemon's API: every client it accepts may do
  anything, as described below
- Denial of service attacks requiring physical or privileged access to the host
- Issues in third-party dependencies with no exploitable path in this project

## Security Considerations for Users

Dicer runs virtual machines and requires elevated host privileges. Operators should:

- Run Dicer on a hardened Linux host with an up-to-date kernel, and keep Dicer
  updated to the latest release
- **Treat access to the API as root on the host.** The API has no users or
  roles: every client the daemon accepts can do anything, including reading
  any file on the host by mounting it into a guest
  (`--mount type=file,...`), since the daemon reads it as root
- Keep the Unix socket as it is set up: owned by root and not readable by
  others (`api.socket.mode`). Whoever can open it controls the daemon
- Leave the TCP listener (`api.tcp.listen`) unset unless you need it. When
  you serve it:
  - Set `api.tcp.tls` with a certificate and key, so traffic is encrypted
    (TLS 1.3). Without it the listener is plaintext and anyone who can reach
    it controls the host: only bind it to loopback, behind an SSH tunnel
  - Set `api.tcp.tls.client_ca_file` to a certificate authority used only
    for Dicer clients. Without it, TLS encrypts but lets in any client
  - There is no revocation list: a client certificate is trusted until it
    expires. Issue short-lived ones, and replace the authority to shut a
    client out sooner
  - Bind it to a private interface or VPN address where you can, and never
    expose it to the internet
- Keep the daemon's configuration file and its TLS private key writable and
  readable by root only: whoever can change the configuration decides who
  may use the API
- Know what the audit log does and does not record: every call that can change
  something is logged with its method and result, but not who made it. Give
  each person or system its own client certificate
- Serve the Prometheus metrics endpoint (`metrics.listen`) only where your
  monitoring can reach it: it is unauthenticated. It exposes counts and
  sizes, and the names of networks, but not what guests contain
- Treat any untrusted workload as potentially hostile to the host boundary:
  workloads run as root inside their guest
- Put instances that must not reach each other on separate networks, or on
  an `--isolated` one
- Remember what passes through the host in the clear: the contents of `file`
  mounts and instances' environment are in the runtime directory (root-only)
  while the instance runs, and registry credentials are in the Docker
  configuration file the daemon reads

## Disclosure Policy

We follow a coordinated disclosure model. We ask that reporters give us reasonable time to develop and release a fix before public disclosure. We will always credit reporters (with their consent) in our security advisories.
