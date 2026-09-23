// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/cli/printer"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

type printableNetwork struct {
	Networks []*dicerdv1.Network
}

func (p *printableNetwork) Cols() []string {
	return []string{"ID", "Name", "Subnet", "Gateway", "Bridge", "Nameservers", "MTU", "Isolated", "Usage", "Created"}
}

func (p *printableNetwork) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Networks))
	for _, n := range p.Networks {
		kv = append(kv, map[string]any{
			"ID":          n.GetId(),
			"Name":        n.GetName(),
			"Subnet":      n.GetSubnet(),
			"Gateway":     n.GetGateway(),
			"Bridge":      n.GetBridge(),
			"Nameservers": strings.Join(n.GetNameservers(), ","),
			"MTU":         n.GetMtu(),
			"Isolated":    n.GetIsolated(),
			"Usage":       formatIPUsage(n.GetTotalIps(), n.GetFreeIps()),
			"Created":     age(timeOf(n.GetCreateTime())),
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

			n, err := client.CreateNetwork(cmd.Context(), &dicerdv1.CreateNetworkRequest{
				Name:        args[0],
				Subnet:      subnet,
				Gateway:     gateway,
				Nameservers: nameservers,
				Mtu:         mtu,
				Isolated:    isolated,
			})
			if err != nil {
				return err
			}

			succeeded(cmd, "Network %s created (%s, gateway %s, bridge %s)",
				n.GetName(), n.GetSubnet(), n.GetGateway(), n.GetBridge())
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

			resp, err := client.ListNetworks(cmd.Context(), &dicerdv1.ListNetworksRequest{})
			if err != nil {
				return err
			}

			return render(cmd, &printableNetwork{Networks: resp.GetNetworks()})
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}

func newNetworkShowCommand() *cobra.Command {
	return newShowCommand(showSpec[*dicerdv1.Network]{
		use:   "show NAME",
		short: "Show a network",
		arg:   "a network name",
		list:  listNetworks,
		get: func(ctx context.Context, client *dicer.Client, name string) (*dicerdv1.Network, error) {
			return client.GetNetwork(ctx, &dicerdv1.GetNetworkRequest{Name: name})
		},
		printable: func(v *dicerdv1.Network) printer.Printable {
			return &printableNetwork{Networks: []*dicerdv1.Network{v}}
		},
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
				if _, err := client.DeleteNetwork(cmd.Context(), &dicerdv1.DeleteNetworkRequest{Name: name}); err != nil {
					return err
				}

				succeeded(cmd, "Network %s deleted", name)
				return nil
			})
		},
	}
}

type printableNetworkAllocation struct {
	Allocations []*dicerdv1.NetworkAllocation
}

func (p *printableNetworkAllocation) Cols() []string {
	return []string{"Instance", "IP", "MAC", "TAP"}
}

func (p *printableNetworkAllocation) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Allocations))
	for _, a := range p.Allocations {
		// The daemon resolves instance names; fall back to the ID for an
		// allocation whose instance has since been removed.
		instance := a.GetInstanceName()
		if instance == "" {
			instance = a.GetInstanceId()
		}

		kv = append(kv, map[string]any{
			"Instance": instance,
			"IP":       a.GetIp(),
			"MAC":      a.GetMac(),
			"TAP":      a.GetTapDevice(),
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

			resp, err := client.ListNetworkAllocations(cmd.Context(),
				&dicerdv1.ListNetworkAllocationsRequest{Name: args[0]})
			if err != nil {
				return err
			}

			return render(cmd, &printableNetworkAllocation{Allocations: resp.GetAllocations()})
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}
