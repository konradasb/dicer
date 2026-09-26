#!/usr/bin/env bash
# Copyright 2026 Dicer Authors
# SPDX-License-Identifier: MIT

#
# Tests the packages GoReleaser built, and the repository build/repo makes of
# them, in containers: installs the package from the repository, with its
# signatures checked, on each distribution the documentation names, as it
# says to, then installs it again and removes it.
#
#   build/test-packages.sh DIST
#
# DIST holds the packages, as `make packages` leaves them in dist/. Those
# for this machine's architecture are tested. The repository is signed with
# a key made for the test, which also signs the RPMs, as the release's key
# does. Needs docker.
#

set -euo pipefail

dist=$(cd "$1" && pwd)
root=$(cd "$(dirname "$0")/.." && pwd)

case $(uname -m) in
x86_64 | amd64) deb_arch=amd64 rpm_arch=x86_64 ;;
aarch64 | arm64) deb_arch=arm64 rpm_arch=aarch64 ;;
*) echo "no packages for $(uname -m)" >&2 && exit 1 ;;
esac

work=$(mktemp -d)
net=dicer-test-packages-$$
server=$net-repo
cleanup() {
  docker rm -f "$server" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

mkdir -p "$work/incoming" "$work/repo"
cp "$dist"/*_"$deb_arch".deb "$dist"/*."$rpm_arch".rpm "$work/incoming/"

echo "--- Building the repository"
# As root, so what it writes is given back to whoever runs this, to remove.
docker run --rm -v "$work:/work" -v "$root/build/repo:/build/repo:ro" \
  -e OWNER="$(id -u):$(id -g)" ubuntu:24.04 bash -euc '
  apt-get update -qq >/dev/null
  apt-get install -y -qq apt-utils createrepo-c gpg rpm >/dev/null
  gpg --batch --quiet --passphrase "" --quick-gen-key "Dicer Test <test@example.invalid>" ed25519 sign never
  rpmsign --define "__gpg /usr/bin/gpg" --define "_gpg_name Dicer Test" \
    --addsign /work/incoming/*.rpm >/dev/null 2>&1
  /build/repo/build.sh /work/incoming /work/repo
  chmod -R a+rX /work/repo
  chown -R "$OWNER" /work
'

docker network create "$net" >/dev/null
docker run -d --name "$server" --network "$net" --network-alias pkg \
  -v "$work/repo:/usr/share/nginx/html:ro" nginx:alpine >/dev/null

# Each client adds the repository as the installation guide does, with
# http://pkg in place of https://pkg.dicer.sh, then installs the package and
# checks what it put where. Without systemd running, as in a container, the
# package must still install and remove cleanly.
check='
  test -x /usr/bin/dicer
  test -x /usr/bin/dicerd
  test -f /usr/lib/systemd/system/dicerd.service
  test -f /etc/dicerd/config.yaml
  getent group dicer >/dev/null
  dicer --help >/dev/null
  dicerd --help >/dev/null
'

apt_client='
  apt-get update -qq >/dev/null
  apt-get install -y -qq curl gpg >/dev/null
  install -d -m 0755 /etc/apt/keyrings
  curl -fsSL http://pkg/gpg.key | gpg --dearmor -o /etc/apt/keyrings/dicer.gpg
  echo "deb [signed-by=/etc/apt/keyrings/dicer.gpg] http://pkg/deb stable main" \
    > /etc/apt/sources.list.d/dicer.list
  apt-get update -qq
  apt-get install -y -qq dicer >/dev/null
  '"$check"'
  apt-get install -y -qq --reinstall dicer >/dev/null
  apt-get purge -y -qq dicer >/dev/null
  test ! -e /usr/bin/dicerd
  test ! -e /etc/dicerd
'

dnf_client='
  curl -fsSL http://pkg/rpm/dicer.repo | sed "s#https://pkg.dicer.sh#http://pkg#" \
    > /etc/yum.repos.d/dicer.repo
  dnf install -y -q dicer
  rpm -q iptables-nft >/dev/null
  '"$check"'
  dnf reinstall -y -q dicer
  dnf remove -y -q dicer
  test ! -e /usr/bin/dicerd
'

zypper_client='
  curl -fsSL http://pkg/rpm/dicer.repo | sed "s#https://pkg.dicer.sh#http://pkg#" \
    > /tmp/dicer.repo
  zypper -q addrepo /tmp/dicer.repo
  zypper -q -n --gpg-auto-import-keys install dicer
  '"$check"'
  zypper -q -n install -f dicer
  zypper -q -n remove dicer
  test ! -e /usr/bin/dicerd
'

clients=(
  "debian:stable|$apt_client"
  "ubuntu:24.04|$apt_client"
  "fedora:latest|$dnf_client"
  "rockylinux/rockylinux:9|dnf install -y -q epel-release; $dnf_client"
  "opensuse/tumbleweed|$zypper_client"
)

failed=()
for client in "${clients[@]}"; do
  image=${client%%|*}
  script=${client#*|}
  echo "--- $image"
  if ! docker run --rm --network "$net" "$image" bash -euxc "$script" >"$work/log" 2>&1; then
    tail -30 "$work/log"
    failed+=("$image")
  fi
done

if ((${#failed[@]})); then
  echo "failed on: ${failed[*]}" >&2
  exit 1
fi
echo "--- All passed"
