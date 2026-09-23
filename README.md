# Dicer

Run virtual machines from container images, on one host.

Dicer pulls an OCI image, converts it to a read-only root filesystem and boots
it as a VM under Cloud Hypervisor or Firecracker, with a writable overlay on
top.

## Requirements

- Linux with KVM (`/dev/kvm`)
- `erofs-utils` (`mkfs.erofs`) and `e2fsprogs` (`mke2fs`)
- IPv4 forwarding enabled

## Install

```console
curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash
```

This builds `dicer` and `dicerd` from source and installs `dicerd` as a
systemd service.

## Quickstart

Create a network and import [Dicer's kernel](https://github.com/konradasb/dicer-kernel)
(on arm64, use `--arch aarch64`, `Image-arm64` and its checksum from the
release's `SHA256SUMS`):

```console
$ dicer network create default --subnet 172.20.0.0/16
$ dicer kernel import linux-6.18 --arch x86_64 \
    --url https://github.com/konradasb/dicer-kernel/releases/download/v6.18.53-1/vmlinux-x86_64 \
    --sha256 ca5db6c291deb8a409db1f1ab14cc55ef6d35504daf17fc0f5577ffc1662b669
```

With one network and one kernel, instances use them by default:

```console
$ dicer run --name web -p 8080:80 nginx:1.27
$ dicer ps
$ dicer exec web sh
$ dicer logs -f web
$ dicer stop web
$ dicer rm web
```

See `dicer --help` for every command.

## Configuration

`dicerd` reads `/etc/dicerd/config.yaml`. The file is optional: every key has
a default, and unknown keys are rejected. See [`example.yml`](example.yml) for
every option.

## License

MIT. See [LICENSE](LICENSE).
