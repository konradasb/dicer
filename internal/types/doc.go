// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package types is the daemon's model of what it manages: instances,
// images, networks, volumes, kernels, snapshots and the events about them.
//
// It is the daemon's alone. Clients see the API's messages in
// proto/dicerd/v1, which internal/grpcapi converts these to and from; the
// daemon's stores write these to disk as their tags say.
package types
