// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/cli/printer"
)

type printableKernel struct {
	Kernels []dicer.Kernel
}

func (p *printableKernel) Columns() []string {
	return []string{"ID", "Name", "Arch", "SHA256", "Created"}
}

func (p *printableKernel) Rows() []map[string]any {
	rows := make([]map[string]any, 0, len(p.Kernels))
	for _, k := range p.Kernels {
		rows = append(rows, map[string]any{
			"ID":      k.ID,
			"Name":    k.Name,
			"Arch":    string(k.Architecture),
			"SHA256":  orDash(k.SHA256),
			"Created": age(k.CreateTime),
		})
	}
	return rows
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
		Use:   "import NAME FILE",
		Short: "Import a kernel to boot instances with",
		Long: "Imports a kernel that instances can boot. FILE is a kernel on this\n" +
			"machine, of up to 512 MiB. It is sent to the daemon, and the command\n" +
			"returns once the kernel is on the daemon's host. If --sha256 is given, the\n" +
			"kernel is checked against it. An instance that names no kernel boots the\n" +
			"default kernel, which needs no import.",
		Example: "  dicer kernel import k6 ./vmlinux --arch x86_64",
		Args:    needs([]string{"a name for the kernel", "the kernel's file"}),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, file := args[0], args[1]
			archFlag, _ := cmd.Flags().GetString("arch")
			sha256, _ := cmd.Flags().GetString("sha256")

			arch, err := parseChoice("--arch", archFlag, architectures)
			if err != nil {
				return usagef(cmd, "%s", err)
			}

			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()

			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			spec := dicer.KernelSpec{Name: name, Architecture: arch, SHA256: sha256}
			k, err := client.Kernels.Import(cmd.Context(), spec, f)
			if err != nil {
				return err
			}

			succeeded(cmd, "Kernel %s imported", k.Name)
			return nil
		},
	}

	cmd.Flags().String("arch", "", "Kernel architecture, e.g. x86_64")
	cmd.Flags().String("sha256", "", "Expected SHA-256 of the kernel, hex-encoded")
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

			list, err := client.Kernels.List(cmd.Context())
			if err != nil {
				return err
			}

			return render(cmd, &printableKernel{Kernels: list})
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}

func newKernelShowCommand() *cobra.Command {
	return newShowCommand(showSpec[dicer.Kernel]{
		use:   "show NAME",
		short: "Show a kernel",
		arg:   "a kernel name",
		list:  listKernels,
		get: func(ctx context.Context, client *dicer.Client, name string) (dicer.Kernel, error) {
			return client.Kernels.Get(ctx, name)
		},
		printable: func(v dicer.Kernel) printer.Printable { return &printableKernel{Kernels: []dicer.Kernel{v}} },
	})
}

func newKernelDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete (NAME... | --all)",
		Short: "Delete one or more kernels no instance uses, or all of them",
		Long: "Deletes the kernels named, or with --all every kernel, asking first on a\n" +
			"terminal. A kernel an instance is defined to boot is refused, and so is the\n" +
			"default kernel, which --all leaves alone.",
		Args:              namesOrAll("kernel name"),
		Aliases:           []string{"rm", "remove"},
		ValidArgsFunction: complete(0, withoutDefault(listKernels)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return eachNameOrAll(cmd, args, withoutDefault(listKernels), "kernels", func(client *dicer.Client, name string) error {
				if err := client.Kernels.Delete(cmd.Context(), name); err != nil {
					return err
				}

				succeeded(cmd, "Kernel %s deleted", name)
				return nil
			})
		},
	}
	addDeleteAllFlags(cmd, "kernels")

	return cmd
}
