#!/bin/bash
# Copyright 2026 Dicer Authors
# SPDX-License-Identifier: MIT

#
# Dicer Install Script
#
# Clones the repository and builds from source using make.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash -s -- --ref v0.2.0
#

set -e

REPO_URL="https://github.com/konradasb/dicer.git"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/dicerd"
DATA_DIR="/var/lib/dicer"
CONFIG_FILE="${CONFIG_DIR}/config.yaml"
SYSTEMD_DIR="/etc/systemd/system"
SERVICE_NAME="dicerd"

# Colors for output (true color)
RED='\033[38;2;255;110;110m'
GREEN='\033[38;2;92;190;83m'
YELLOW='\033[0;33m'
BLUE='\033[38;2;86;156;214m'
NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC}  $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC}  $1"; }
error() {
  echo -e "${RED}[ERROR]${NC} $1"
  exit 1
}

usage() {
  cat <<EOF
Usage: install.sh [OPTIONS]

Options:
  --ref REF    Git ref to build (branch, tag, or commit; default: main)
  --help       Show this help message

Examples:
  # Install from main branch
  curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash

  # Install from a specific tag
  curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash -s -- --ref v0.2.0

  # Install from a feature branch
  curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash -s -- --ref my-feature-branch
EOF
}

# =============================================================================
# Argument parsing
# =============================================================================

REF="main"

while [[ $# -gt 0 ]]; do
  case "$1" in
  --ref)
    [ -n "${2:-}" ] || error "--ref requires a value."
    REF="$2"
    shift 2
    ;;
  --help)
    usage
    exit 0
    ;;
  *)
    error "Unknown option: $1. Run with --help for usage."
    ;;
  esac
done

# =============================================================================
# Pre-flight checks
# =============================================================================

info "Running pre-flight checks..."

# Require root or sudo
SUDO=""
if [ "$EUID" -ne 0 ]; then
  if ! command -v sudo >/dev/null 2>&1; then
    error "This script requires root privileges. Please run as root or install sudo."
  fi
  if ! sudo -n true 2>/dev/null; then
    info "Requesting sudo privileges..."
    # shellcheck disable=SC2024 # redirect is intentional: read password from real terminal, not piped stdin
    if ! sudo -v </dev/tty; then
      error "Failed to obtain sudo privileges."
    fi
  fi
  SUDO="sudo"
fi

# Required tools
command -v git >/dev/null 2>&1 || error "git is required but not installed."
command -v go >/dev/null 2>&1 || error "go is required but not installed."
command -v make >/dev/null 2>&1 || error "make is required but not installed."
command -v curl >/dev/null 2>&1 || error "curl is required but not installed."
command -v systemctl >/dev/null 2>&1 || error "systemctl is required (systemd not available?)."
command -v mkfs.erofs >/dev/null 2>&1 || error "mkfs.erofs is required but not installed. Please install erofs-utils package."
command -v mkfs.ext4 >/dev/null 2>&1 || error "mkfs.ext4 is required but not installed. Please install the e2fsprogs package."
command -v mke2fs >/dev/null 2>&1 || error "mke2fs is required but not installed. Please install the e2fsprogs package."

# Linux only
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
[ "$OS" = "linux" ] || error "Dicer only supports Linux (detected: $OS)."

# Supported architectures
ARCH=$(uname -m)
case "$ARCH" in
x86_64 | amd64) ARCH="amd64" ;;
aarch64 | arm64) ARCH="arm64" ;;
*) error "Unsupported architecture: $ARCH (supported: amd64, arm64)." ;;
esac

# KVM is required
[ -e /dev/kvm ] || error "/dev/kvm not found — KVM is required for Dicer to function."

info "Pre-flight checks passed."

# =============================================================================
# System configuration
# =============================================================================

# IPv4 forwarding (required for VM networking)
CURRENT_IP_FORWARD=$(sysctl -n net.ipv4.ip_forward 2>/dev/null || echo "0")
if [ "$CURRENT_IP_FORWARD" != "1" ]; then
  info "Enabling IPv4 forwarding..."
  $SUDO sysctl -w net.ipv4.ip_forward=1 >/dev/null
  if [ -d /etc/sysctl.d ]; then
    echo 'net.ipv4.ip_forward=1' | $SUDO tee /etc/sysctl.d/99-dicer.conf >/dev/null
  elif ! grep -q '^net.ipv4.ip_forward=1' /etc/sysctl.conf 2>/dev/null; then
    echo 'net.ipv4.ip_forward=1' | $SUDO tee -a /etc/sysctl.conf >/dev/null
  fi
fi

# File descriptor limits
if [ -d /etc/security/limits.d ] && [ ! -f /etc/security/limits.d/99-dicer.conf ]; then
  info "Configuring file descriptor limits..."
  $SUDO tee /etc/security/limits.d/99-dicer.conf >/dev/null <<'EOF'
# Dicer: increased file descriptor limits
*     soft  nofile  65536
*     hard  nofile  65536
root  soft  nofile  65536
root  hard  nofile  65536
EOF
fi

# =============================================================================
# Clone and build from source
# =============================================================================

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

info "Cloning repository (ref: ${REF})..."
git clone --depth 1 --branch "$REF" "$REPO_URL" "${TMP_DIR}/dicer" ||
  error "Failed to clone repository at ref '${REF}'. Check that the ref exists."

info "Building from source (this may take a few minutes)..."
make -C "${TMP_DIR}/dicer" build ||
  error "Build failed. Check that Go and all build dependencies are installed."

# =============================================================================
# Stop existing service (if running)
# =============================================================================

if $SUDO systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
  info "Stopping existing ${SERVICE_NAME} service..."
  $SUDO systemctl stop "$SERVICE_NAME"
fi

# =============================================================================
# Install binaries
# =============================================================================

info "Installing dicer to ${INSTALL_DIR}/dicer..."
$SUDO install -m 755 "${TMP_DIR}/dicer/bin/dicer" "${INSTALL_DIR}/dicer"

info "Installing dicerd to ${INSTALL_DIR}/dicerd..."
$SUDO install -m 755 "${TMP_DIR}/dicer/bin/dicerd" "${INSTALL_DIR}/dicerd"

# =============================================================================
# Create directories
# =============================================================================

info "Creating config directory at ${CONFIG_DIR}..."
$SUDO mkdir -p "$CONFIG_DIR"

info "Creating data directory at ${DATA_DIR}..."
$SUDO mkdir -p "$DATA_DIR"

# =============================================================================
# Create the dicer group
# =============================================================================

# Members of the dicer group can use the daemon's socket without sudo. It
# starts empty: adding a user gives them as much as root on this host.
if ! getent group dicer >/dev/null 2>&1; then
  info "Creating the dicer group..."
  $SUDO groupadd --system dicer
fi

# =============================================================================
# Write config
# =============================================================================

if [ ! -f "$CONFIG_FILE" ]; then
  info "Writing config file at ${CONFIG_FILE}..."
  $SUDO tee "$CONFIG_FILE" >/dev/null <<EOF
data_dir: ${DATA_DIR}
run_dir: /run/dicer
api:
  socket:
    path: /run/dicer/dicer.sock
    mode: 0660
    group: dicer

log_level: info
EOF
  $SUDO chmod 640 "$CONFIG_FILE"
else
  info "Config file already exists at ${CONFIG_FILE}."
fi

# =============================================================================
# Install systemd service
# =============================================================================

info "Installing systemd service..."
$SUDO tee "${SYSTEMD_DIR}/${SERVICE_NAME}.service" >/dev/null <<EOF
[Unit]
Description=Dicer Daemon
Documentation=https://github.com/konradasb/dicer
After=network.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/dicerd serve --config ${CONFIG_FILE}
Restart=on-failure
RestartSec=5
# Running VMs are separate processes and outlive the daemon; recovery
# re-adopts them on the next start.
KillMode=process

# Creates and owns /run/dicer, which holds the API socket and runtime state.
# Being a tmpfs, it is cleared on reboot -- which is correct, since no
# instance survives one. It must survive the service stopping, though: it
# holds the state of the VMs that outlive the daemon, which the next one
# needs to adopt them. Without Preserve, systemd deletes it on every stop.
RuntimeDirectory=dicer
RuntimeDirectoryMode=0755
RuntimeDirectoryPreserve=yes

# Security hardening
ProtectSystem=strict
PrivateTmp=true
ReadWritePaths=${DATA_DIR}

[Install]
WantedBy=multi-user.target
EOF

info "Reloading systemd..."
$SUDO systemctl daemon-reload

info "Enabling ${SERVICE_NAME} service..."
$SUDO systemctl enable "$SERVICE_NAME"

info "Starting ${SERVICE_NAME} service..."
$SUDO systemctl start "$SERVICE_NAME"

# =============================================================================
# Done
# =============================================================================

echo ""
echo -e "${BLUE}"
cat <<'EOF'
  ██████╗  ██╗   ██████╗  ███████╗ ██████╗
  ██╔══██╗ ██║  ██╔════╝  ██╔════╝ ██╔══██╗
  ██║  ██║ ██║  ██║       █████╗   ██████╔╝
  ██║  ██║ ██║  ██║       ██╔══╝   ██╔══██╗
  ██████╔╝ ██║  ╚██████╗  ███████╗ ██║  ██║
  ╚═════╝  ╚═╝   ╚═════╝  ╚══════╝ ╚═╝  ╚═╝
EOF
echo -e "${NC}"
info "Dicer installed successfully!"
echo ""
echo "Next steps — create a network and import a kernel:"
echo ""
echo "  dicer network create default \\"
echo "    --subnet 172.20.0.0/16 --gateway 172.20.0.1 --nameservers 8.8.8.8,1.1.1.1"
echo ""
case "$ARCH" in
amd64)
  echo "  dicer kernel import linux-6.18 --arch x86_64 \\"
  echo "    --url https://github.com/konradasb/dicer-kernel/releases/download/v6.18.53-1/vmlinux-x86_64 \\"
  echo "    --sha256 ca5db6c291deb8a409db1f1ab14cc55ef6d35504daf17fc0f5577ffc1662b669"
  KERNEL_NAME="linux-6.18"
  ;;
arm64)
  echo "  dicer kernel import linux-6.18 --arch aarch64 \\"
  echo "    --url https://github.com/konradasb/dicer-kernel/releases/download/v6.18.53-1/Image-arm64 \\"
  echo "    --sha256 1ce335854bc05535584dd57638f10832db91c4a20cbb76bab7851890c3d14568"
  KERNEL_NAME="linux-6.18"
  ;;
esac
echo ""
echo "Then define and start your first instance:"
echo ""
echo "  dicer image pull ubuntu:24.04"
echo ""
echo "  dicer instance create test1 \\"
echo "    --image ubuntu:24.04 --kernel ${KERNEL_NAME} --network default \\"
echo "    --vcpus 1 --memory 2GiB --disk 10GiB"
echo ""
echo "  dicer instance start test1"
echo "  dicer instance exec test1 -- bash"
echo ""
echo "The commands above need sudo. To run dicer without it, join the dicer group,"
echo "which gives as much as root on this host, then log in again:"
echo ""
echo "  sudo usermod -aG dicer \$USER"
echo ""
echo "Full help: dicer --help"
echo ""
echo "Join our community for support and updates: https://dicer.sh/community"
echo -e "${YELLOW}Thank you for installing Dicer!${NC}"
echo ""
