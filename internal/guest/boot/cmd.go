// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/version"
)

// NewCommand returns the root command for the guest init process.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "dicer-init",
		Short:         "Boot a Dicer guest: runs as PID 1 inside the VM",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       version.Version,
		Args:          cobra.NoArgs,
		RunE:          runRoot,
	}

	cmd.SetVersionTemplate(version.String() + "\n")
	cmd.AddCommand(newEntrypointCommand())

	cmd.Flags().StringP("config", "c", guest.ConfigFile, "Config filename on the config disk ("+configDiskDevice+")")

	return cmd
}

func runRoot(cmd *cobra.Command, _ []string) error {
	// Before logging is available, since the console is under /dev. Mounting
	// what is already mounted fails harmlessly.
	mountEarlyFilesystems()

	file, path := openConsole()
	os.Stdout = file
	os.Stderr = file
	log := slog.New(slog.NewTextHandler(&console{path: path, file: file}, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	configFile, err := cmd.Flags().GetString("config")
	if err != nil {
		return fmt.Errorf("get config flag: %w", err)
	}

	boot(log, configFile)
	return nil
}
