// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package defaults is where Dicer lives on a host unless configured
// otherwise: the locations more than one of its packages or programs must
// agree on, such as the socket the daemon serves on and a client looks at.
// Settings only one package uses stay with it.
package defaults

const (
	// DataDir holds what persists: definitions, images and disks.
	DataDir = "/var/lib/dicer"

	// RunDir holds runtime state. It should be a tmpfs, so that a reboot
	// clears it.
	RunDir = "/run/dicer"

	// Socket is where the daemon serves its API, and where a client looks
	// for it.
	Socket = RunDir + "/dicer.sock"

	// Config is the daemon's configuration file.
	Config = "/etc/dicerd/config.yaml"
)
