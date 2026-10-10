---
title: Routing with Caddy
weight: 7
description: "Put Caddy in front of your instances, and route each site to the instances whose labels match, as they start and stop."
icon: switch-horizontal
related:
  - /docs/guides/remote-access
  - /docs/guides/health-checks
  - /docs/guides/running-workloads
---

[Caddy](https://caddyserver.com) can route each of your sites to the
instances whose labels match a selector, using the
[caddy-dicer](https://github.com/kazysgurskas/caddy-dicer) plugin. Caddy
spreads requests across the matching instances. It also follows the
daemon's events, so an instance starts getting requests as soon as it is
running and healthy, and stops getting them when it stops. You never need to
reload Caddy for this.

In this example, three instances of a shop serve `shop.example.com`, and a
single blog instance serves `blog.example.com`. Caddy runs on the Dicer
host and gets certificates for both sites from Let's Encrypt.

## Build Caddy with the plugin

Caddy plugins are compiled into the Caddy binary. Build Caddy with this
plugin using [xcaddy](https://github.com/caddyserver/xcaddy), which needs Go:

```console
$ go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
$ xcaddy build --with github.com/kazysgurskas/caddy-dicer
$ ./caddy list-modules | grep dicer
http.reverse_proxy.upstreams.dicer
```

If you build on a different machine, set the target system to match the
host, for example `GOOS=linux GOARCH=amd64 xcaddy build ...` or
`GOARCH=arm64`.

On Debian and Ubuntu, install Caddy's package first, which sets up the
service and the `caddy` user. Then replace the package's binary with your
build, as shown in
[Caddy's documentation](https://caddyserver.com/docs/build#package-support-files-for-custom-builds-for-debianubunturaspbian):

```console
$ sudo dpkg-divert --divert /usr/bin/caddy.default --rename /usr/bin/caddy
$ sudo mv ./caddy /usr/bin/caddy.custom
$ sudo update-alternatives --install /usr/bin/caddy caddy /usr/bin/caddy.default 10
$ sudo update-alternatives --install /usr/bin/caddy caddy /usr/bin/caddy.custom 50
```

Your build stays in place when the package is upgraded.

## Give Caddy a token

Caddy only needs to list instances and follow their events. Turn on the
daemon's TCP listener on the loopback address only, and give Caddy a
[token](../../guides/remote-access#limit-what-a-token-may-do) with just
those two permissions:

```yaml {filename="/etc/dicerd/config.yaml"}
server:
  listen: 127.0.0.1:7443
```

```console
$ sudo systemctl restart dicerd
$ sudo dicer token create caddy --scopes instances:read,events:read |
    sudo install -m 600 -o caddy -g caddy /dev/stdin /etc/caddy/dicer-token
```

The `events:read` scope lets Caddy see events for every resource on the
host, not only instances, but it cannot change anything.

Caddy could use the daemon's socket instead, without a token, but only if
the `caddy` user is in the `dicer` group. That gives Caddy as much power as
root, so the token is the safer choice.

## Run the instances

Label each instance with the app it runs, and give it a
[health check](../../guides/health-checks). Run the first shop instance,
then [fork](../../guides/snapshots#forking-an-instance) it for the other
two:

```console
$ dicer run -d --name shop-1 --label app=shop --health-http 3000/healthz ghcr.io/acme/shop:3
$ dicer fork shop-1 shop-2
$ dicer fork shop-1 shop-3
$ dicer run -d --name blog --label app=blog --health-http 8080 ghcr.io/acme/blog:1
```

A fork has the same definition as the instance it was forked from,
including its labels and health check, but its own name and address. It
also starts with a copy of that instance's memory, so the shop is already
running in it, with nothing to boot or warm up.

The instances don't publish any ports. Caddy connects to each one directly,
at its address on the Dicer network.

## Route to them

For each site, give the label selector that picks its instances, and the
port they listen on:

```caddyfile {filename="/etc/caddy/Caddyfile"}
(dicer) {
	address 127.0.0.1:7443
	token   {file./etc/caddy/dicer-token}
}

shop.example.com {
	reverse_proxy {
		dynamic dicer app=shop 3000 {
			import dicer
		}
		lb_policy least_conn
		lb_try_duration 5s
	}
}

blog.example.com {
	reverse_proxy {
		dynamic dicer app=blog 8080 {
			import dicer
		}
	}
}
```

```console
$ sudo systemctl reload caddy
$ curl https://shop.example.com
```

A selector can match on more than one label, for example
`app=shop,tier in (frontend,edge),!canary`. The plugin's
[README](https://github.com/kazysgurskas/caddy-dicer#selectors) describes
the full syntax. With `lb_try_duration`, a request that reaches an instance
just as it stops is retried on another instance instead of failing.

## Add and remove instances

Fork another shop instance, and Caddy starts sending it requests once its
health check passes:

```console
$ dicer fork shop-1 shop-4
```

The instance you fork from is paused for about a second while its memory is
copied. Caddy retries requests that arrive meanwhile on the other instances,
because of `lb_try_duration`. To avoid pausing a serving instance at all,
take a [snapshot](../../guides/snapshots) of it once, and fork that
whenever you need another instance:

```console
$ dicer snapshot create shop-1 shop-warm
$ dicer snapshot fork shop-warm shop-5
```

Stop or delete an instance, and Caddy stops sending it requests:

```console
$ dicer stop shop-2
```

The same happens when an instance is paused, or when its health check finds
it unhealthy. If a site has no instances left to route to, Caddy answers
`503 Service Unavailable`.

## Good to know

- Caddy must be able to reach the instances' network, and by default only
  the Dicer host can. Run Caddy on the host, or on a machine with a route to
  that network.
- An instance on [standby](../../guides/standby) gets no requests, and a
  request through Caddy does not wake it up. Only a connection to one of the
  instance's published ports does that. Don't set `--standby-after` on
  instances behind Caddy.
- While the daemon restarts, Caddy keeps routing to the instances it already
  knows about, because they keep running. Once the daemon is back, Caddy
  picks up any changes.
- One Caddy can route to instances on several hosts. For each site, point
  the plugin at the [listener](../../guides/remote-access#serve-the-api-over-tcp)
  of the host its instances run on, with a token from that host. Caddy also
  needs a network route to each host's instances.
