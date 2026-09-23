// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package dicer is what a Dicer daemon manages, and the client that manages
// it.
//
// The two belong together because they are two views of one thing. The types
// here -- Instance, Image, Volume, Network, Kernel -- are what the daemon
// stores and what the client returns, so a program built on Dicer imports one
// package and speaks one vocabulary. The generated protobuf messages are not
// part of it: they are how the two talk, converted at each edge and seen by
// neither side's code.
//
// # The client
//
// With no options, a client talks to the daemon on this machine over its
// socket:
//
//	c, err := dicer.NewClient()
//	defer c.Close()
//
//	instances, err := c.ListInstances(ctx)
//
// A daemon on another machine is reached over mutual TLS, pinned to its
// certificate. There is no certificate authority: each side generates a key
// that never leaves its machine, and recognises the other by the fingerprint
// of its certificate, as SSH does. A client is trusted once its key is
// enrolled with a token, which Enroll spends:
//
//	token, err := dicer.ParseJoinToken(encoded)
//	identity, err := dicer.LoadIdentity(dir)
//	remote, err := dicer.Enroll(ctx, token, identity)
//
//	c, err := dicer.NewClient(dicer.WithRemote(remote), dicer.WithIdentity(identity))
//
// # Instances
//
// An Instance is a virtual machine in two halves. Spec is what was asked for
// and persists; Status is what the host made of it and is empty after a
// reboot, because nothing is running then. Creating one records a spec and
// boots nothing:
//
//	inst, err := c.CreateInstance(ctx, dicer.InstanceSpec{
//		Name:        "web",
//		ImageRef:    "nginx:1.27",
//		VCPUs:       2,
//		MemoryBytes: 1 << 30,
//		Ports:       []dicer.PortMapping{{HostPort: 8080, GuestPort: 80}},
//	})
//	inst, err = c.StartInstance(ctx, inst.Spec.Name)
//
// # Errors
//
// What went wrong is matched, not read. The daemon puts an error in a class,
// the class travels as a status code, and the client puts it back:
//
//	if _, err := c.GetInstance(ctx, name); errors.Is(err, dicer.ErrNotFound) {
//		...
//	}
package dicer
