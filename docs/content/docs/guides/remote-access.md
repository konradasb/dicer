---
title: Remote access
weight: 8
description: "Serve the API over TCP with TLS, and point the command line at another host."
icon: lock-closed
related:
  - /docs/guides/using-the-api
  - /docs/reference/configuration
  - /docs/reference/files-and-environment
---

The daemon serves its API on a Unix socket, for the machine it runs on. It
can also serve it over TCP, so that `dicer`, or any gRPC client, can manage
the host from elsewhere. This guide sets that up with TLS and client
certificates, and points the command line at it.

{{< callout type="warning" >}}
  The API has no users or roles: whoever can reach it can do anything the
  daemon can, which is as much as root on the host. Guard it as you would
  root SSH access.
{{< /callout >}}

## On the host itself

The socket, `/run/dicer/dicer.sock`, belongs to root, and only root can
open it, so use `sudo` on the host:

```console
$ sudo dicer ps
```

## Serve the API over TCP

### 1. Make certificates

You need a certificate authority of your own, used only for Dicer; a
certificate for the daemon, from it, that names the address clients connect
to; and a certificate for each client, from it. With OpenSSL:

```bash
# The certificate authority
openssl req -x509 -new -nodes -days 3650 \
  -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
  -keyout ca-key.pem -out ca.pem -subj "/CN=Dicer CA"

# The daemon, named as clients will reach it
openssl req -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
  -keyout server-key.pem -out server.csr -subj "/CN=dicer1.example.com"
openssl x509 -req -in server.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial \
  -days 825 -out server.pem -extfile <(printf '%s\n' \
    "subjectAltName=DNS:dicer1.example.com,IP:192.0.2.10" \
    "extendedKeyUsage=serverAuth")

# A client
openssl req -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
  -keyout client-key.pem -out client.csr -subj "/CN=alice"
openssl x509 -req -in client.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial \
  -days 825 -out client.pem -extfile <(printf '%s\n' "extendedKeyUsage=clientAuth")
```

The daemon's certificate must name, in `subjectAltName`, every name and
address clients will use; its common name is not checked. Keep `ca-key.pem`
somewhere safe, away from the host: it can make certificates the daemon will
accept.

### 2. Configure the daemon

Copy `server.pem`, `server-key.pem` and `ca.pem` to the host, then set the
listener in the daemon's [configuration](../../reference/configuration):

```yaml {filename="/etc/dicerd/config.yaml"}
api:
  tcp:
    listen: 0.0.0.0:7443
    tls:
      cert_file: /etc/dicerd/tls/server.pem
      key_file: /etc/dicerd/tls/server-key.pem
      client_ca_file: /etc/dicerd/tls/ca.pem
```

```console
$ sudo chmod 600 /etc/dicerd/tls/server-key.pem
$ sudo systemctl restart dicerd
$ sudo dicer info | grep 'Network API'
    Network API: 0.0.0.0:7443
```

Restarting the daemon leaves running guests alone. Open the port in the
host's firewall, for the addresses clients connect from.

With `client_ca_file` set, the daemon accepts only clients with a
certificate from that authority, and turns away any other before it gets a
word in. Connections use TLS 1.3.

### 3. Add the host as a remote

On the client, add the daemon as a remote, with the authority that verifies
it and the certificate that identifies the client:

```console
$ dicer remote create prod dicer1.example.com:7443 \
    --tls-ca ~/.dicer/ca.pem \
    --tls-cert ~/.dicer/client.pem --tls-key ~/.dicer/client-key.pem
$ dicer --remote prod info
```

Remotes are kept in `~/.config/dicer/remotes.yaml`, or in
`$DICER_CONFIG_DIR`. The certificate files are only named there, and read
at every connection, so renewing them needs no change to the remote.

`--tls-server-name` gives the name the daemon's certificate must carry, if
the address is not it: an IP address, say, for a certificate that names only
a DNS name.

## Choose which daemon to talk to

Every command goes to one daemon, the first of:

1. `--remote NAME`, or `-r NAME`, on the command;
2. `$DICER_REMOTE`;
3. the current remote, set with `dicer remote use`;
4. `local`, the daemon on this machine.

```console
$ dicer remote use prod        # from now on, commands go to prod
$ dicer ps
$ DICER_REMOTE=local dicer ps  # this once, the local daemon
$ dicer remote use local       # back to the local daemon
```

`dicer remote list` shows the remotes and which is current, and `dicer info`
shows which one a command reaches. Everything works over a remote as it
does locally, `exec`, `cp` and `logs -f` included. A path given to `cp` or
`--env-file` is read on the client; one given to a `file` mount, on the
daemon's host.

`--remote` also takes an address, for a one-off connection: `unix:///PATH`
or `HOST:PORT`. An address alone carries no TLS settings, so it suits only a
socket or a daemon without TLS.

## Take a client's access away

The daemon trusts every certificate its authority has made until it
expires; there is no list of revoked ones. To shut a client out before then,
make a new authority, give the daemon and every other client certificates
from it, and restart the daemon. Short-lived client certificates make this
rarely necessary.

`dicer remote delete` only forgets a remote on the client.

## Without TLS

Without `tls`, the listener is plaintext and unauthenticated: anyone who can
reach the port controls the host, and can read everything sent. Use it only
on a network that reaches no one you would not give root, such as a
loopback address behind an SSH tunnel:

```yaml
api:
  tcp:
    listen: 127.0.0.1:7443
```

```console
$ ssh -N -L 7443:127.0.0.1:7443 dicer1.example.com &
$ dicer --remote 127.0.0.1:7443 ps
```

With `cert_file` and `key_file` but no `client_ca_file`, the listener is
encrypted but lets in any client. That suits a daemon certificate from a
public authority, with access controlled some other way. A remote for it
needs `--tls-ca`, which can be the system's bundle, such as
`/etc/ssl/certs/ca-certificates.crt`: a remote with neither `--tls-ca` nor
`--tls-cert` connects in plaintext.
