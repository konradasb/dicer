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
#   curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash -s -- --ref v0.3.0
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
  curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash -s -- --ref v0.3.0

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
command -v zstd >/dev/null 2>&1 || error "zstd is required but not installed. Please install the zstd package."
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

# Earlier versions of this script raised every login's file descriptor limit
# here. limits.d applies only to login sessions, so it never reached the
# service, which now sets its own with LimitNOFILE. Remove the file if it is
# still the one they wrote.
LIMITS_FILE=/etc/security/limits.d/99-dicer.conf
if [ -f "$LIMITS_FILE" ] && head -n 1 "$LIMITS_FILE" | grep -qx '# Dicer: increased file descriptor limits'; then
  info "Removing ${LIMITS_FILE}, which earlier versions wrote..."
  $SUDO rm -f "$LIMITS_FILE"
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
# The packages' own unit, so that the two cannot drift apart. Only the
# binary's path differs: the packages put dicerd in /usr/bin.
sed "s|^ExecStart=/usr/bin/dicerd |ExecStart=${INSTALL_DIR}/dicerd |" \
  "${TMP_DIR}/dicer/build/package/dicerd.service" |
  $SUDO tee "${SYSTEMD_DIR}/${SERVICE_NAME}.service" >/dev/null
grep -q "^ExecStart=${INSTALL_DIR}/dicerd " "${SYSTEMD_DIR}/${SERVICE_NAME}.service" ||
  error "The service file from ${REF} has an ExecStart this script does not recognise."

# =============================================================================
# Install the firewalld zone
# =============================================================================

# dicerd binds each network's bridge to the dicer zone where firewalld runs,
# which reads it when it reloads.
if [ -d /etc/firewalld/zones ]; then
  info "Installing the dicer firewalld zone..."
  $SUDO install -m 644 "${TMP_DIR}/dicer/build/package/firewalld-zone.xml" /etc/firewalld/zones/dicer.xml
  if $SUDO firewall-cmd --state >/dev/null 2>&1; then
    $SUDO firewall-cmd --reload >/dev/null
  fi
fi

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
echo "Next steps — run your first instance, and open a shell in it:"
echo ""
echo "  dicer run -d --name web -p 8080:80 nginx:1.27"
echo "  dicer exec web"
echo ""
echo "The commands above need sudo. To run dicer without it, join the dicer group,"
echo "which gives as much as root on this host, then log in again:"
echo ""
echo "  sudo usermod -aG dicer \$USER"
echo ""
echo "Full help: dicer --help"
echo "Documentation: https://dicer.sh/docs/"
echo ""
echo -e "${YELLOW}Thank you for installing Dicer!${NC}"
echo ""
