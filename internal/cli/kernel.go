// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/cli/printer"
)

type printableKernel struct {
	Kernels []dicer.Kernel
}

func (p *printableKernel) Cols() []string {
	return []string{"ID", "Name", "Arch", "URL", "SHA256", "Created"}
}

func (p *printableKernel) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Kernels))
	for _, k := range p.Kernels {
		kv = append(kv, map[string]any{
			"ID":      k.ID,
			"Name":    k.Name,
			"Arch":    k.Arch,
			"URL":     k.URL,
			"SHA256":  orDash(k.SHA256),
			"Created": age(k.CreatedAt),
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
			arch, _ := cmd.Flags().GetString("arch")
			sha256, _ := cmd.Flags().GetString("sha256")

			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			k, err := client.ImportKernel(cmd.Context(), dicer.Kernel{
				Name:   args[0],
				URL:    url,
				Arch:   arch,
				SHA256: sha256,
			})
			if err != nil {
				return err
			}

			succeeded(cmd, "Kernel %s imported", k.Name)
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

			kernels, err := client.ListKernels(cmd.Context())
			if err != nil {
				return err
			}

			return render(cmd, &printableKernel{Kernels: kernels})
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}

func newKernelShowCommand() *cobra.Command {
	return newShowCommand(showSpec[dicer.Kernel]{
		use:       "show NAME",
		short:     "Show a kernel",
		arg:       "a kernel name",
		list:      listKernels,
		get:       (*dicer.Client).GetKernel,
		printable: func(v dicer.Kernel) printer.Printable { return &printableKernel{Kernels: []dicer.Kernel{v}} },
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
				if err := client.DeleteKernel(cmd.Context(), name); err != nil {
					return err
				}

				succeeded(cmd, "Kernel %s deleted", name)
				return nil
			})
		},
	}
}
