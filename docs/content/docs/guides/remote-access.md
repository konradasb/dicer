---
title: Remote access
weight: 9
description: "Serve the API over TCP with TLS, and point the command line at another host."
icon: lock-closed
related:
  - /docs/guides/using-the-api
  - /docs/reference/configuration
  - /docs/reference/files-and-environment
---

The daemon serves its API on a Unix socket, for the machine it runs on. It
can also serve the API over TCP, so that `dicer`, or any gRPC client, can
manage the host from elsewhere. This guide sets that up with TLS and client
certificates, and points the command line at it.

{{< callout type="warning" >}}
  The API has no users or roles: whoever can reach it can do anything the
  daemon can, which is as much as root on the host. Guard it as you would
  root SSH access.
{{< /callout >}}

## On the host itself

The socket, `/run/dicer/dicer.sock`, belongs to root and to the `dicer`
group. The packages and `install.sh` create the group with no members. Use
`sudo` on the host, or join the group and log in again:

```console
$ sudo usermod -aG dicer $USER
$ dicer ps
```

The group's members can do anything with the daemon, which is as much as root
on the host, so add only people you would give root.
[`api.socket.group`](../../reference/configuration#api-socket-group) in the
daemon's configuration names the group. Without it, the socket is root's
alone. A configuration file written before the setting existed does not have
it: add `group: dicer` under `api.socket`, and restart the daemon.

## Serve the API over TCP

### 1. Make certificates

You need three kinds of certificate:

- a certificate authority of your own, used only for Dicer;
- a certificate for the daemon, issued by that authority, that names the
  address clients connect to;
- a certificate for each client, issued by the same authority.

With OpenSSL:

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

The daemon's certificate must name every name and address clients will use
in its `subjectAltName`. Its common name is not checked. Keep `ca-key.pem`
somewhere safe, away from the host, because it can make certificates the
daemon will accept.

### 2. Configure the daemon

Copy `server.pem`, `server-key.pem` and `ca.pem` to `/etc/dicerd/tls/` on the
host, then set the listener in the daemon's
[configuration](../../reference/configuration#api-tcp):

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
$ dicer info | grep 'Network API'
    Network API: 0.0.0.0:7443
```

Restarting the daemon leaves running guests alone. Open the port in the
host's firewall for the addresses clients connect from.

With `client_ca_file` set, the daemon accepts only clients with a
certificate from that authority. It turns any other client away during the
TLS handshake, before it can make a call. Connections use TLS 1.3.

The daemon reloads its own certificate and key when they change on disk, so
renewing them needs no restart. It reads `client_ca_file` only when it
starts.

### 3. Add the host as a remote

On the client, add the daemon as a remote, with the authority that verifies
it and the certificate that identifies the client:

```console
$ dicer remote create prod dicer1.example.com:7443 \
    --tls-ca ~/.dicer/ca.pem \
    --tls-cert ~/.dicer/client.pem --tls-key ~/.dicer/client-key.pem
$ dicer --remote prod info
```

Remotes are kept in `remotes.yaml`, in `$DICER_CONFIG_DIR` if it is set.
Otherwise it is in `~/.config/dicer` on Linux (or `$XDG_CONFIG_HOME/dicer`),
and in `~/Library/Application Support/dicer` on macOS. The file only names
the certificate files.
They are read at every connection, so renewing them needs no change to the
remote.

If the address is not a name the daemon's certificate carries, give one that
it does carry with `--tls-server-name`. For example, an IP address needs it
when the certificate names only a DNS name.

## Choose which daemon to talk to

Every command goes to one daemon, the first of these that is set:

1. `--remote NAME`, or `-r NAME`, on the command.
2. `$DICER_REMOTE`.
3. The current remote, set with `dicer remote use`.
4. `local`, the daemon on this machine.

```console
$ dicer remote use prod        # from now on, commands go to prod
$ dicer ps
$ DICER_REMOTE=local dicer ps  # this once, the local daemon
$ dicer remote use local       # back to the local daemon
```

`dicer remote list` shows the remotes and which is current. `dicer info`
shows which one a command reaches.

Everything works over a remote as it does locally, `exec`, `cp` and
`logs -f` included. A path given to `cp` or `--env-file` is read on the
client. A path given to a `file` mount is read on the daemon's host.

`--remote` and `$DICER_REMOTE` also take an address, for a one-off
connection: `unix:///PATH` or `HOST:PORT`. An address alone carries no TLS
settings, so it suits only a socket or a daemon without TLS.

## Connections that go quiet

When a daemon's host loses power, or its network drops, nothing closes the
connection, and the client still sees it as open. So a client pings the
daemon every 30 seconds while the connection carries nothing. If no answer
comes within 10 seconds, it gives the connection up and fails the calls on
it, rather than leaving them to wait.

The daemon does the same for its clients, as set by
[`api.keepalive`](../../reference/configuration#api-keepalive). It lets a
client ping as often as every 10 seconds, and disconnects one that pings
more often. A program using the Go package sets its own timings with
`dicer.WithKeepalive`.

A connection over the host's socket is never pinged, because the kernel
closes it if either end goes.

## Take a client's access away

The daemon trusts every certificate its authority has issued until it
expires. There is no list of revoked certificates. To shut a client out
before its certificate expires, make a new authority, and give the daemon
and every other client certificates from it. Then restart the daemon, which
reads `client_ca_file` only when it starts. Short-lived client certificates
make this rarely necessary.

`dicer remote delete` only removes a remote from the client's list. It does
not change what the daemon accepts.

## Without TLS

Without `tls`, the listener is unencrypted and unauthenticated. Anyone who
can reach the port controls the host, and can read everything sent. Use it
only where no one you would not give root can reach it, such as on a
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
encrypted but lets any client in. That suits a daemon certificate from a
public authority, with access controlled some other way. A remote for such a
daemon needs `--tls-ca`, which can be the system's bundle, such as
`/etc/ssl/certs/ca-certificates.crt`. A remote with neither `--tls-ca` nor
`--tls-cert` connects without TLS.
