// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer/internal/defaults"
	"github.com/konradasb/dicer/internal/version"
)

// NewCommand returns the root command for the dicerd binary.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "dicerd",
		Short:         "Run virtual machines from container images on this host",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       version.Version,
	}

	cmd.SetVersionTemplate(version.String() + "\n")
	cmd.AddCommand(newServeCommand())

	return cmd
}

// newServeCommand returns the serve subcommand, which runs the daemon until
// it is signalled to stop.
func newServeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the daemon",
		Args:  cobra.NoArgs,
		RunE:  runServe,
	}

	cmd.Flags().StringP("config", "f", defaults.Config, "Configuration file; a missing file means all defaults")

	return cmd
}

// runServe loads the configuration and runs the daemon until SIGINT or
// SIGTERM.
func runServe(cmd *cobra.Command, _ []string) error {
	configFile, _ := cmd.Flags().GetString("config")

	config, err := loadConfig(configFile)
	if err != nil {
		return err
	}

	d, err := newDaemon(config)
	if err != nil {
		return fmt.Errorf("new daemon: %w", err)
	}

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return d.Run(ctx)
}
