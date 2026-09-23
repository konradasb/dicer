#!/bin/bash
# Copyright 2026 Dicer Authors
# SPDX-License-Identifier: MIT

#
# Dicer Uninstall Script
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/uninstall.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/uninstall.sh | bash -s -- --purge
#

set -e

INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/dicerd"
DATA_DIR="/var/lib/dicer"
SYSTEMD_DIR="/etc/systemd/system"
SERVICE_NAME="dicerd"

# Colors for output (true color)
RED='\033[38;2;255;110;110m'
GREEN='\033[38;2;92;190;83m'
YELLOW='\033[0;33m'
NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC}  $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC}  $1"; }
error() {
  echo -e "${RED}[ERROR]${NC} $1"
  exit 1
}

usage() {
  cat <<EOF
Usage: uninstall.sh [OPTIONS]

Options:
  --purge    Also remove all data (${DATA_DIR}). Cannot be undone.
  --help     Show this help message

Examples:
  # Uninstall (keep data)
  curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/uninstall.sh | bash

  # Uninstall and remove all data
  curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/uninstall.sh | bash -s -- --purge
EOF
}

# =============================================================================
# Argument parsing
# =============================================================================

PURGE=0

while [[ $# -gt 0 ]]; do
  case "$1" in
  --purge)
    PURGE=1
    shift
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
    # shellcheck disable=SC2024
    if ! sudo -v </dev/tty; then
      error "Failed to obtain sudo privileges."
    fi
  fi
  SUDO="sudo"
fi

command -v systemctl >/dev/null 2>&1 || error "systemctl is required (systemd not available?)."

info "Pre-flight checks passed."

# =============================================================================
# Stop and disable service
# =============================================================================

if $SUDO systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
  info "Stopping ${SERVICE_NAME} service..."
  $SUDO systemctl stop "$SERVICE_NAME"
fi

if $SUDO systemctl is-enabled --quiet "$SERVICE_NAME" 2>/dev/null; then
  info "Disabling ${SERVICE_NAME} service..."
  $SUDO systemctl disable "$SERVICE_NAME"
fi

# =============================================================================
# Remove systemd service file
# =============================================================================

SERVICE_FILE="${SYSTEMD_DIR}/${SERVICE_NAME}.service"
if [ -f "$SERVICE_FILE" ]; then
  info "Removing systemd service file..."
  $SUDO rm -f "$SERVICE_FILE"
  $SUDO systemctl daemon-reload
fi

# =============================================================================
# Remove binaries
# =============================================================================

info "Removing ${INSTALL_DIR}/dicer binary..."
$SUDO rm -f "${INSTALL_DIR}/dicer"

info "Removing ${INSTALL_DIR}/dicerd binary..."
$SUDO rm -f "${INSTALL_DIR}/dicerd"

# =============================================================================
# Remove config directory
# =============================================================================

if [ -d "$CONFIG_DIR" ]; then
  info "Removing config directory ${CONFIG_DIR}..."
  $SUDO rm -rf "$CONFIG_DIR"
fi

# =============================================================================
# Remove data directory
# =============================================================================

if [ "$PURGE" -eq 1 ]; then
  info "Removing data directory ${DATA_DIR}..."
  $SUDO rm -rf "$DATA_DIR"
else
  warn "Data directory ${DATA_DIR} was not removed."
  warn "Run with --purge to also delete all Dicer data (images, instances, volumes)."
fi

# =============================================================================
# Remove system configuration files
# =============================================================================

if [ -f /etc/sysctl.d/99-dicer.conf ]; then
  info "Removing /etc/sysctl.d/99-dicer.conf..."
  $SUDO rm -f /etc/sysctl.d/99-dicer.conf
fi

if [ -f /etc/security/limits.d/99-dicer.conf ]; then
  info "Removing /etc/security/limits.d/99-dicer.conf..."
  $SUDO rm -f /etc/security/limits.d/99-dicer.conf
fi

# =============================================================================
# Done
# =============================================================================

echo ""
info "Dicer uninstalled successfully."
if [ "$PURGE" -eq 0 ] && [ -d "$DATA_DIR" ]; then
  echo ""
  echo "  Data preserved at: ${DATA_DIR}"
  echo "  To remove it, run: sudo rm -rf ${DATA_DIR}"
fi
echo ""
