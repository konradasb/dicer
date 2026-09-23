// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/cli/printer"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

type printableKernel struct {
	Kernels []*dicerdv1.Kernel
}

func (p *printableKernel) Cols() []string {
	return []string{"ID", "Name", "Arch", "URL", "SHA256", "Created"}
}

func (p *printableKernel) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Kernels))
	for _, k := range p.Kernels {
		kv = append(kv, map[string]any{
			"ID":      k.GetId(),
			"Name":    k.GetName(),
			"Arch":    archName(k.GetArch()),
			"URL":     k.GetUrl(),
			"SHA256":  orDash(k.GetSha256()),
			"Created": age(timeOf(k.GetCreateTime())),
		})
	}
	return kv
}

func newKernelCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "kernel",
		Short:   "Manage guest kernels",
		Aliases: []string{"kernels"},
	}

	cmd.AddCommand(
		newKernelImportCommand(),
		newKernelListCommand(),
		newKernelShowCommand(),
		newKernelDeleteCommand(),
	)

	return cmd
}

func newKernelImportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import NAME",
		Short: "Record a kernel to boot instances with",
		Long: "Records a kernel by URL. It is downloaded, and verified against --sha256\n" +
			"if given, the first time an instance boots with it.",
		Args: one("a name for the kernel"),
		RunE: func(cmd *cobra.Command, args []string) error {
			url, _ := cmd.Flags().GetString("url")
			archFlag, _ := cmd.Flags().GetString("arch")
			sha256, _ := cmd.Flags().GetString("sha256")

			arch, err := parseEnum[dicerdv1.Architecture]("--arch", archFlag)
			if err != nil {
				return usagef(cmd, "%s", err)
			}

			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			k, err := client.ImportKernel(cmd.Context(), &dicerdv1.ImportKernelRequest{
				Name:   args[0],
				Url:    url,
				Arch:   arch,
				Sha256: sha256,
			})
			if err != nil {
				return err
			}

			succeeded(cmd, "Kernel %s imported", k.GetName())
			return nil
		},
	}

	cmd.Flags().String("url", "", "Where to fetch the kernel: an http(s) URL, a file:// URL or an absolute path")
	cmd.Flags().String("arch", "", "Kernel architecture, e.g. x86_64")
	cmd.Flags().String("sha256", "", "Expected SHA-256 of the kernel, hex-encoded")
	requireFlag(cmd, "url", "https://example.com/vmlinux")
	requireFlag(cmd, "arch", "x86_64")

	return cmd
}

func newKernelListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List kernels",
		Args:    noArgs,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			resp, err := client.ListKernels(cmd.Context(), &dicerdv1.ListKernelsRequest{})
			if err != nil {
				return err
			}

			return render(cmd, &printableKernel{Kernels: resp.GetKernels()})
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}

func newKernelShowCommand() *cobra.Command {
	return newShowCommand(showSpec[*dicerdv1.Kernel]{
		use:   "show NAME",
		short: "Show a kernel",
		arg:   "a kernel name",
		list:  listKernels,
		get: func(ctx context.Context, client *dicer.Client, name string) (*dicerdv1.Kernel, error) {
			return client.GetKernel(ctx, &dicerdv1.GetKernelRequest{Name: name})
		},
		printable: func(v *dicerdv1.Kernel) printer.Printable { return &printableKernel{Kernels: []*dicerdv1.Kernel{v}} },
	})
}

func newKernelDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "delete NAME...",
		Short:             "Delete one or more kernels no instance uses",
		Args:              oneOrMore("kernel name"),
		Aliases:           []string{"rm", "remove"},
		ValidArgsFunction: complete(0, listKernels),
		RunE: func(cmd *cobra.Command, args []string) error {
			return eachName(cmd, args, listKernels, func(client *dicer.Client, name string) error {
				if _, err := client.DeleteKernel(cmd.Context(), &dicerdv1.DeleteKernelRequest{Name: name}); err != nil {
					return err
				}

				succeeded(cmd, "Kernel %s deleted", name)
				return nil
			})
		},
	}
}
