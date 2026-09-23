// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/cli/remote"
)

type printableRemote struct {
	Remotes []namedRemote
}

// namedRemote is a remote with its name, and whether it is the current one.
type namedRemote struct {
	name    string
	remote  dicer.Remote
	current bool
}

func (p *printableRemote) Cols() []string {
	return []string{"Name", "Address", "Fingerprint", "Current"}
}

func (p *printableRemote) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Remotes))
	for _, r := range p.Remotes {
		current := ""
		if r.current {
			current = "*"
		}
		kv = append(kv, map[string]any{
			"Name":        r.name,
			"Address":     r.remote.Address,
			"Fingerprint": orDash(r.remote.Fingerprint),
			"Current":     current,
		})
	}
	return kv
}

func newRemoteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "remote",
		Short:   "Manage the daemons this client talks to",
		Aliases: []string{"remotes"},
		Long: "Manage the daemons this client talks to.\n\n" +
			"The built-in remote \"" + remote.Local + "\" is the daemon on this machine. A daemon on " +
			"another is added with an enrolment token from 'dicer token create' run against it, " +
			"which also has it trust this client.",
	}

	cmd.AddCommand(
		newRemoteCreateCommand(),
		newRemoteListCommand(),
		newRemoteDeleteCommand(),
		newRemoteUseCommand(),
	)

	return cmd
}

func newRemoteCreateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "create NAME TOKEN|ADDRESS",
		Short: "Add a daemon to talk to",
		Long: "Add a daemon to talk to.\n\n" +
			"Given an enrolment token from 'dicer token create', connect to the daemon it names, " +
			"check it against the fingerprint in the token, and enrol this client's key with it. " +
			"The token is spent. Given a unix:// address, just record it.",
		Example: "  dicer remote create prod dicer1.eyJuYW1lIjoi...\n" +
			"  dicer remote create test unix:///run/dicer-test/dicer.sock",
		Args:    needs([]string{"a name for the remote", "an enrolment token or a unix:// address"}),
		Aliases: []string{"new", "add"},
		RunE: func(cmd *cobra.Command, args []string) error {
			name, credential := args[0], args[1]

			dir, err := remote.Dir()
			if err != nil {
				return err
			}
			cfg, err := remote.Load(dir)
			if err != nil {
				return err
			}

			// Checked before a token is spent on a name that cannot be
			// saved.
			if _, err := cfg.Get(name); err == nil {
				return fmt.Errorf("remote %q already exists", name)
			}

			var r dicer.Remote
			if dicer.IsAddress(credential) {
				r, err = dicer.ParseAddress(credential)
				if err != nil {
					return err
				}
			} else {
				join, err := dicer.ParseJoinToken(credential)
				if err != nil {
					return err
				}
				r, err = enroll(cmd, join)
				if err != nil {
					return err
				}
				succeeded(cmd, "Enrolled with the daemon at %s as %q", r.HostPort(), join.Name)
			}

			if err := cfg.Create(name, r); err != nil {
				return err
			}
			if err := cfg.Save(dir); err != nil {
				return err
			}

			succeeded(cmd, "Remote %s created. Use it with: dicer remote use %s", name, name)
			return nil
		},
	}
}

// enroll enrols this client's key with the daemon a token names, and returns
// the remote it succeeded at. It is dicer.Enroll with the CLI's key and its
// tracing.
func enroll(cmd *cobra.Command, join dicer.JoinToken) (dicer.Remote, error) {
	identity, err := clientIdentity()
	if err != nil {
		return dicer.Remote{}, err
	}

	return dicer.Enroll(cmd.Context(), join, identity,
		dicer.WithDialOptions(dialOptions(cmd, "")...))
}

func newRemoteListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List remotes",
		Args:    noArgs,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := remote.Dir()
			if err != nil {
				return err
			}
			cfg, err := remote.Load(dir)
			if err != nil {
				return err
			}

			current := cfg.CurrentName()
			p := &printableRemote{}
			for _, name := range cfg.Names() {
				r, _ := cfg.Get(name)
				p.Remotes = append(p.Remotes, namedRemote{name: name, remote: r, current: name == current})
			}

			return render(cmd, p)
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}

func newRemoteDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete NAME",
		Short: "Forget a remote",
		Long: "Forget a remote.\n\n" +
			"The daemon goes on trusting this client until it is removed there, " +
			"with 'dicer client delete'.",
		Args:              one("a remote name"),
		Aliases:           []string{"rm", "remove"},
		ValidArgsFunction: completeRemotes,
		RunE: func(cmd *cobra.Command, args []string) error {
			return updateRemotes(func(cfg *remote.Config) error {
				if err := cfg.Delete(args[0]); err != nil {
					return err
				}
				succeeded(cmd, "Remote %s deleted", args[0])
				return nil
			})
		},
	}
}

func newRemoteUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use NAME",
		Short: "Make a remote the current one",
		Long: "Make a remote the one commands talk to unless told otherwise " +
			"with --remote or $" + remoteEnv + ".",
		Args:              one("a remote name"),
		ValidArgsFunction: completeRemotes,
		RunE: func(cmd *cobra.Command, args []string) error {
			return updateRemotes(func(cfg *remote.Config) error {
				if err := cfg.Use(args[0]); err != nil {
					return err
				}
				succeeded(cmd, "Now using remote %s", args[0])
				return nil
			})
		},
	}
}

// updateRemotes loads the remotes, changes them and saves them.
func updateRemotes(change func(*remote.Config) error) error {
	dir, err := remote.Dir()
	if err != nil {
		return err
	}
	cfg, err := remote.Load(dir)
	if err != nil {
		return err
	}

	if err := change(cfg); err != nil {
		return err
	}

	return cfg.Save(dir)
}
