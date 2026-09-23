// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/cli/printer"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

type printableImage struct {
	Images []*dicerdv1.Image
}

func (p *printableImage) Cols() []string {
	return []string{"Name", "Digest", "Size", "Created", "Last used"}
}

func (p *printableImage) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Images))
	for _, img := range p.Images {
		kv = append(kv, map[string]any{
			"Name":      img.GetName(),
			"Digest":    img.GetDigest(),
			"Size":      size(img.GetSizeBytes()),
			"Created":   age(timeOf(img.GetCreateTime())),
			"Last used": age(timeOf(img.GetLastUsedTime())),
		})
	}
	return kv
}

// shortDigest abbreviates a digest for a person, as git does a commit:
// "sha256:1a2b3c4d5e6f".
func shortDigest(digest string) string {
	algo, hex, ok := strings.Cut(digest, ":")
	if !ok || len(hex) <= 12 {
		return digest
	}
	return algo + ":" + hex[:12]
}

func newImageCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage images",
		// Not "images": that is 'dicer images', the list.
	}

	cmd.AddCommand(
		newImagePullCommand(),
		newImageListCommand(),
		newImageShowCommand(),
		newImageDeleteCommand(),
		newImagePruneCommand(),
	)

	return cmd
}

func newImagePullCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "pull REF",
		Short:             "Pull an image and convert it to a bootable disk",
		Args:              one("an image"),
		ValidArgsFunction: complete(1, listImages),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			reporter := newPullReporter(cmd.OutOrStdout())
			defer reporter.done()

			start := time.Now()
			img, err := pullImage(cmd.Context(), client, args[0], reporter.report)
			if err != nil {
				return err
			}
			reporter.done()

			if !reporter.fetched {
				succeeded(cmd, "Image %s is up to date (%s)", img.GetName(), shortDigest(img.GetDigest()))

				return nil
			}
			succeeded(cmd, "Image %s pulled in %s (%s, %s)", img.GetName(),
				formatDuration(time.Since(start)), shortDigest(img.GetDigest()), size(img.GetSizeBytes()))

			return nil
		},
	}
}

func newImageListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List pulled images",
		Args:    noArgs,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			resp, err := client.ListImages(cmd.Context(), &dicerdv1.ListImagesRequest{})
			if err != nil {
				return err
			}

			return render(cmd, &printableImage{Images: resp.GetImages()})
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}

func newImageShowCommand() *cobra.Command {
	return newShowCommand(showSpec[*dicerdv1.Image]{
		use:   "show REF",
		short: "Show a pulled image",
		arg:   "an image",
		list:  listImages,
		get: func(ctx context.Context, client *dicer.Client, ref string) (*dicerdv1.Image, error) {
			return client.GetImage(ctx, &dicerdv1.GetImageRequest{Ref: ref})
		},
		printable: func(v *dicerdv1.Image) printer.Printable { return &printableImage{Images: []*dicerdv1.Image{v}} },
	})
}

func newImageDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "delete REF...",
		Short:             "Delete one or more unused images",
		Args:              oneOrMore("image"),
		Aliases:           []string{"rm", "remove"},
		ValidArgsFunction: complete(0, listImages),
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")

			return eachName(cmd, args, listImages, func(client *dicer.Client, ref string) error {
				if _, err := client.DeleteImage(cmd.Context(), &dicerdv1.DeleteImageRequest{Ref: ref, Force: force}); err != nil {
					return withHint(err, codes.FailedPrecondition, "use -f to delete it anyway")
				}

				succeeded(cmd, "Image %s deleted", ref)
				return nil
			})
		},
	}

	cmd.Flags().BoolP("force", "f", false, "Force deletion of an image that is in use by one or more instances")

	return cmd
}

func newImagePruneCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete every unused image",
		Long: "Deletes every unused image, and the cached layers that make them bootable. " +
			"Images that are in use by one or more instances are not deleted. " +
			"Use -f to skip confirmation.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if force, _ := cmd.Flags().GetBool("force"); !force {
				ok, err := confirm(cmd, "Delete every unused image?")
				if err != nil || !ok {
					return err
				}
			}

			client, cleanup, err := newClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			result, err := client.PruneImages(cmd.Context(), &dicerdv1.PruneImagesRequest{})
			if err != nil {
				return err
			}

			for _, img := range result.GetImages() {
				succeeded(cmd, "Deleted %s", img.GetName())
			}
			succeeded(cmd, "Reclaimed %s from %d image(s)",
				size(result.GetReclaimedBytes()), len(result.GetImages()))

			return nil
		},
	}

	cmd.Flags().BoolP("force", "f", false, "Do not ask for confirmation")

	return cmd
}
