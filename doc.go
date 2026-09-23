// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package dicer connects Go programs to a Dicer daemon.
//
// The API itself is the gRPC service in proto/dicerd/v1, and its generated
// code is what every client uses, whatever its language. This package only
// makes the connection: to the local daemon's socket by default, or to a
// daemon's TCP listener, with TLS.
//
//	c, err := dicer.NewClient()
//	if err != nil {
//		return err
//	}
//	defer c.Close()
//
//	inst, err := c.GetInstance(ctx, &dicerdv1.GetInstanceRequest{Name: "web"})
//
// A daemon on another machine is named by its address, a gRPC target:
//
//	c, err := dicer.NewClient(dicer.WithAddress("host:7443"), dicer.WithTLS(cfg))
//
// The socket is protected by its file permissions. A TCP listener is
// unauthenticated unless the daemon is configured with TLS.
//
// Errors are gRPC statuses, whose codes the service's documentation gives:
//
//	if status.Code(err) == codes.NotFound {
//		...
//	}
package dicer
