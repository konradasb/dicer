// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/cli/printer"
)

type printableNetwork struct {
	Networks []dicer.Network
}

func (p *printableNetwork) Cols() []string {
	return []string{"ID", "Name", "Subnet", "Gateway", "Bridge", "Nameservers", "MTU", "Isolated", "Usage", "Created"}
}

func (p *printableNetwork) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Networks))
	for _, n := range p.Networks {
		kv = append(kv, map[string]any{
			"ID":          n.ID,
			"Name":        n.Name,
			"Subnet":      n.Subnet,
			"Gateway":     n.Gateway,
			"Bridge":      n.Bridge,
			"Nameservers": strings.Join(n.Nameservers, ","),
			"MTU":         n.MTU,
			"Isolated":    n.Isolated,
			"Usage":       formatIPUsage(n.TotalIPs, n.FreeIPs),
			"Created":     age(n.CreatedAt),
		})
	}
	return kv
}

// formatIPUsage renders address usage as "used/total (percent%)".
func formatIPUsage(total, free int64) string {
	if total <= 0 {
		return "0/0 (0%)"
	}
	used := total - free
	return fmt.Sprintf("%d/%d (%d%%)", used, total, used*100/total)
}

func newNetworkCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "network",
		Short:   "Manage networks",
		Aliases: []string{"networks"},
	}

	cmd.AddCommand(
		newNetworkCreateCommand(),
		newNetworkListCommand(),
		newNetworkShowCommand(),
		newNetworkDeleteCommand(),
		newNetworkAllocationCommand(),
	)

	return cmd
}

func newNetworkCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "create NAME",
		Short:   "Create a network",
		Args:    one("a name for the network"),
		Aliases: []string{"new"},
		RunE: func(cmd *cobra.Command, args []string) error {
			subnet, _ := cmd.Flags().GetString("subnet")
			gateway, _ := cmd.Flags().GetString("gateway")
			nameservers, _ := cmd.Flags().GetStringSlice("nameservers")
			mtu, _ := cmd.Flags().GetInt32("mtu")
			isolated, _ := cmd.Flags().GetBool("isolated")

			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			n, err := client.CreateNetwork(cmd.Context(), dicer.Network{
				Name:        args[0],
				Subnet:      subnet,
				Gateway:     gateway,
				Nameservers: nameservers,
				MTU:         int(mtu),
				Isolated:    isolated,
			})
			if err != nil {
				return err
			}

			succeeded(cmd, "Network %s created (%s, gateway %s, bridge %s)",
				n.Name, n.Subnet, n.Gateway, n.Bridge)
			return nil
		},
	}

	cmd.Flags().String("subnet", "", "Subnet in CIDR notation, e.g. 172.20.0.0/16")
	cmd.Flags().String("gateway", "", "Gateway address (default: the first address in the subnet)")
	cmd.Flags().StringSlice("nameservers", nil, "DNS servers for guests, comma-separated (default: the daemon's)")
	cmd.Flags().Int32("mtu", 0, "MTU (default: the daemon's)")
	cmd.Flags().Bool("isolated", false, "Stop instances on the network reaching each other")
	requireFlag(cmd, "subnet", "172.20.0.0/16")

	return cmd
}

func newNetworkListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List networks",
		Args:    noArgs,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			networks, err := client.ListNetworks(cmd.Context())
			if err != nil {
				return err
			}

			return render(cmd, &printableNetwork{Networks: networks})
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}

func newNetworkShowCommand() *cobra.Command {
	return newShowCommand(showSpec[dicer.Network]{
		use:       "show NAME",
		short:     "Show a network",
		arg:       "a network name",
		list:      listNetworks,
		get:       (*dicer.Client).GetNetwork,
		printable: func(v dicer.Network) printer.Printable { return &printableNetwork{Networks: []dicer.Network{v}} },
	})
}

func newNetworkDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "delete NAME...",
		Short:             "Delete one or more networks no instance uses",
		Args:              oneOrMore("network name"),
		Aliases:           []string{"rm", "remove"},
		ValidArgsFunction: complete(0, listNetworks),
		RunE: func(cmd *cobra.Command, args []string) error {
			return eachName(cmd, args, listNetworks, func(client *dicer.Client, name string) error {
				if err := client.DeleteNetwork(cmd.Context(), name); err != nil {
					return err
				}

				succeeded(cmd, "Network %s deleted", name)
				return nil
			})
		},
	}
}

type printableNetworkAllocation struct {
	Allocations []dicer.NetworkAllocation
}

func (p *printableNetworkAllocation) Cols() []string {
	return []string{"Instance", "IP", "MAC", "TAP"}
}

func (p *printableNetworkAllocation) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Allocations))
	for _, a := range p.Allocations {
		// The daemon resolves instance names; fall back to the ID for an
		// allocation whose instance has since been removed.
		instance := a.InstanceName
		if instance == "" {
			instance = a.InstanceID
		}

		kv = append(kv, map[string]any{
			"Instance": instance,
			"IP":       a.IP,
			"MAC":      a.MAC,
			"TAP":      a.TAPDevice,
		})
	}
	return kv
}

func newNetworkAllocationCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "allocation",
		Short:   "Inspect the addresses assigned on a network",
		Aliases: []string{"allocations", "alloc"},
	}

	cmd.AddCommand(newNetworkAllocationListCommand())

	return cmd
}

func newNetworkAllocationListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "list NETWORK",
		Short:             "List the addresses assigned on a network",
		Args:              one("a network name"),
		Aliases:           []string{"ls"},
		ValidArgsFunction: complete(1, listNetworks),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			allocations, err := client.ListNetworkAllocations(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			return render(cmd, &printableNetworkAllocation{Allocations: allocations})
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}
