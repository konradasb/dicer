// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
)

// accessLong explains, for 'dicer client' and 'dicer token' alike, how a
// daemon decides who may use it.
const accessLong = "On the daemon's socket, its file permissions decide who may use it. Over TCP, only " +
	"clients enrolled with a token from 'dicer token create' are served, each trusted by " +
	"the fingerprint of its own key."

type printableClient struct {
	Clients []dicer.TrustedClient
}

func (p *printableClient) Cols() []string {
	return []string{"Name", "Fingerprint", "Subject", "Expires", "Created"}
}

func (p *printableClient) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Clients))
	for _, c := range p.Clients {
		kv = append(kv, map[string]any{
			"Name":        c.Name,
			"Fingerprint": c.Fingerprint,
			"Subject":     orDash(c.Subject),
			"Expires":     date(c.ExpiresAt),
			"Created":     age(c.CreatedAt),
		})
	}

	return kv
}

type printableToken struct {
	Tokens []dicer.AccessToken
}

func (p *printableToken) Cols() []string {
	return []string{"Name", "Expires", "Created"}
}

func (p *printableToken) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Tokens))
	for _, t := range p.Tokens {
		kv = append(kv, map[string]any{
			"Name":    t.Name,
			"Expires": date(t.ExpiresAt),
			"Created": age(t.CreatedAt),
		})
	}

	return kv
}

// --- Tokens ---

func newTokenCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage the tokens clients enrol with",
		Long:  "Manage the one-time tokens remote clients enrol with.\n\n" + accessLong,
	}

	cmd.AddCommand(
		newTokenCreateCommand(),
		newTokenListCommand(),
		newTokenDeleteCommand(),
	)

	return cmd
}

func newTokenCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a token a client can enrol with",
		Long: "Create a one-time token a client can enrol with, to be trusted as NAME.\n\n" +
			"Hand the token to the client, which runs 'dicer remote create REMOTE TOKEN'. " +
			"The token holds a secret: anyone with it can enrol until it is used or expires.",
		Args:    one("a name for the client"),
		Aliases: []string{"new", "add"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ttl, _ := cmd.Flags().GetDuration("ttl")

			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			token, expires, err := client.CreateToken(cmd.Context(), args[0], ttl)
			if err != nil {
				return err
			}

			cmd.Printf("Enrolment token for %s, valid until %s:\n\n%s\n\nOn the client: dicer remote create REMOTE TOKEN\n",
				args[0], expires.Local().Format(time.DateTime), token)

			return nil
		},
	}

	cmd.Flags().Duration("ttl", dicer.DefaultTokenTTL, "How long the token can be used for")

	return cmd
}

func newTokenListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List tokens not yet used",
		Long:    "List the tokens that can still be enrolled with. Their secrets are not shown: the daemon does not keep them.",
		Args:    noArgs,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			tokens, err := client.ListTokens(cmd.Context())
			if err != nil {
				return err
			}

			return render(cmd, &printableToken{Tokens: tokens})
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}

func newTokenDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "delete NAME",
		Short:   "Withdraw a token before it is used",
		Long:    "Withdraw the token created for NAME, so that nobody can enrol with it.",
		Args:    one("a token name"),
		Aliases: []string{"rm", "remove", "revoke"},
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			if err := client.DeleteToken(cmd.Context(), args[0]); err != nil {
				return err
			}

			cmd.Printf("Token for %s deleted\n", args[0])

			return nil
		},
	}
}

// --- Clients ---

func newClientCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "client",
		Short: "Manage the clients trusted over the network",
		Long:  "Manage the clients this daemon trusts over the network.\n\n" + accessLong,
	}

	cmd.AddCommand(
		newClientListCommand(),
		newClientShowCommand(),
		newClientDeleteCommand(),
	)

	return cmd
}

func newClientListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List trusted clients",
		Args:    noArgs,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			clients, err := client.ListClients(cmd.Context())
			if err != nil {
				return err
			}

			return render(cmd, &printableClient{Clients: clients})
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}

func newClientShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show NAME",
		Short: "Show a trusted client",
		Long: "Show a trusted client, by name or fingerprint.\n\n" +
			"The certificate's subject and expiry are what the client wrote into it, shown for " +
			"reference: only its fingerprint decides trust, and its dates are not enforced.",
		Args:    one("a client name"),
		Aliases: []string{"get", "inspect"},
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			c, err := client.GetClient(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			if pem, _ := cmd.Flags().GetBool("pem"); pem {
				cmd.Print(c.Certificate)

				return nil
			}

			return render(cmd, &printableClient{Clients: []dicer.TrustedClient{c}})
		},
	}

	addOutputFlags(cmd, false)
	cmd.Flags().Bool("pem", false, "Print only the client's certificate, PEM-encoded")

	return cmd
}

func newClientDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "delete NAME",
		Short:   "Stop trusting a client",
		Long:    "Stop trusting a client, by name or fingerprint. Its next request is refused.",
		Args:    one("a client name"),
		Aliases: []string{"rm", "remove", "revoke"},
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			if err := client.DeleteClient(cmd.Context(), args[0]); err != nil {
				return err
			}

			cmd.Printf("Client %s is no longer trusted\n", args[0])

			return nil
		},
	}
}
