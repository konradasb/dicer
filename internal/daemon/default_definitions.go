// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"errors"
	"fmt"
	"time"

	"github.com/nrednav/cuid2"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/hostnet"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/kernel"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
)

// ensureDefaultNetwork creates the default network on the configured subnet,
// unless it already exists. An existing one is left as it is.
func (d *daemon) ensureDefaultNetwork() error {
	if _, err := d.definitions.Network(types.DefaultNetworkName); err == nil {
		return nil
	}

	n, err := network.New(network.Spec{Name: types.DefaultNetworkName, Subnet: d.cfg.Network.DefaultSubnet})
	if err != nil {
		return fmt.Errorf("create the default network: %w", err)
	}
	hostSubnets, err := hostnet.Subnets()
	if err != nil {
		return fmt.Errorf("list the host's subnets: %w", err)
	}
	if err := network.CheckSubnetOverlap(n, d.definitions.Networks(), hostSubnets); err != nil {
		return fmt.Errorf("create the default network: %w; set network.default_subnet to a free subnet", err)
	}
	if err := d.definitions.CreateNetwork(n); err != nil {
		return fmt.Errorf("create the default network: %w", err)
	}

	d.events.Record(events.Event{
		Kind:       events.KindNetwork,
		ID:         n.ID,
		Name:       n.Name,
		Action:     events.ActionCreated,
		Message:    fmt.Sprintf("Created the default network with subnet %s, gateway %s", n.Subnet, n.Gateway),
		Attributes: map[string]string{"subnet": n.Subnet, "gateway": n.Gateway},
	})
	return nil
}

// ensureDefaultKernel puts the default kernel this binary carries on the
// host, and defines it once it is there. It extracts it again if its copy on
// the host is missing or damaged, and in place of the one an older version of
// Dicer carried.
func (d *daemon) ensureDefaultKernel() error {
	want := kernel.Default()
	record := func(k types.Kernel, action events.Action, message string) {
		d.events.Record(events.Event{
			Kind:       events.KindKernel,
			ID:         k.ID,
			Name:       k.Name,
			Action:     action,
			Message:    message,
			Attributes: map[string]string{"arch": k.Architecture},
		})
	}

	k, err := d.definitions.Kernel(types.DefaultKernelName)
	switch {
	case errors.Is(err, errdefs.ErrNotFound):
		now := time.Now()
		want.ID, want.CreatedAt, want.UpdatedAt = cuid2.Generate(), now, now
		if err := d.kernels.ExtractDefault(want.ID); err != nil {
			return err
		}
		if err := d.definitions.CreateKernel(want); err != nil {
			return fmt.Errorf("define the default kernel: %w", err)
		}
		record(want, events.ActionImported, fmt.Sprintf("Imported the default kernel for %s, version %s: %s",
			want.Architecture, kernel.DefaultVersion, humanize.Bytes(d.kernels.DiskBytes(want.ID))))
		return nil
	case err != nil:
		return fmt.Errorf("define the default kernel: %w", err)
	case k.Architecture == want.Architecture && k.SHA256 == want.SHA256:
		if _, err := d.kernels.Path(k); err == nil {
			return nil
		}
		if err := d.kernels.ExtractDefault(k.ID); err != nil {
			return err
		}
		record(k, events.ActionImported,
			"Imported the default kernel again, as its copy on the host was missing or damaged")
		return nil
	}

	if err := d.kernels.ExtractDefault(k.ID); err != nil {
		return err
	}
	k.Architecture, k.SHA256, k.UpdatedAt = want.Architecture, want.SHA256, time.Now()
	if err := d.definitions.UpdateKernel(k); err != nil {
		return fmt.Errorf("update the default kernel: %w", err)
	}
	record(k, events.ActionUpdated, fmt.Sprintf("Updated the default kernel to version %s, which this version of Dicer carries",
		kernel.DefaultVersion))
	return nil
}
