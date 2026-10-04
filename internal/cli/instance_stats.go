// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer/internal/bytesize"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// printableInstanceStats lists what instances use of the host, a row each.
// Its column names are also a --format template's fields and JSON's keys.
type printableInstanceStats struct {
	Instances []*dicerdv1.InstanceStats
}

func (p *printableInstanceStats) Cols() []string {
	return []string{"ID", "Name", "CPUPerc", "MemUsage", "MemPerc", "NetIO", "BlockIO"}
}

func (p *printableInstanceStats) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Instances))
	for _, s := range p.Instances {
		memPerc := "--"
		if s.GetMemoryBytes() > 0 {
			memPerc = fmt.Sprintf("%.2f%%", float64(s.GetResidentMemoryBytes())/float64(s.GetMemoryBytes())*100)
		}
		kv = append(kv, map[string]any{
			"ID":       s.GetId(),
			"Name":     s.GetName(),
			"CPUPerc":  fmt.Sprintf("%.2f%%", s.GetCpuPercent()),
			"MemUsage": bytesize.Format(s.GetResidentMemoryBytes()) + " / " + bytesize.Format(s.GetMemoryBytes()),
			"MemPerc":  memPerc,
			"NetIO":    bytesize.Format(s.GetNetworkReceiveBytes()) + " / " + bytesize.Format(s.GetNetworkTransmitBytes()),
			"BlockIO":  bytesize.Format(s.GetDiskReadBytes()) + " / " + bytesize.Format(s.GetDiskWrittenBytes()),
		})
	}
	return kv
}

func newInstanceStatsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats [NAME...]",
		Short: "Show what instances use of the host, live",
		Long: "Shows each running or paused instance's CPU, memory, network and disk use,\n" +
			"read on the host from its hypervisor process: nothing in the guest is asked.\n" +
			"Without names, every running or paused instance is shown, as they come and\n" +
			"go.\n\n" +
			"CPUPerc is a share of one host CPU, so 200% is two kept busy; the threads\n" +
			"emulating its devices count too. MemUsage is the hypervisor's resident host\n" +
			"memory / the guest memory committed to the instance, and MemPerc the one as\n" +
			"a share of the other. NetIO is what the guest received / transmitted, and\n" +
			"BlockIO what the hypervisor read / wrote: the guest's disks, and its own\n" +
			"files, such as the serial console log and a snapshot's memory. Reads served\n" +
			"from the host's page cache are not counted. Both are totals since the\n" +
			"instance started.\n\n" +
			"The column names are also the fields of a --format template, and the keys of\n" +
			"--format json.\n\n" +
			"The view is redrawn every second until Ctrl+C. With --no-stream it is shown\n" +
			"once, a second after asking, since CPU use is measured over that second.",
		Example: "  dicer stats\n" +
			"  dicer stats web db\n" +
			"  dicer stats --no-stream --format '{{.Name}}\\t{{.CPUPerc}}\\t{{.MemUsage}}'\n" +
			"  dicer stats --no-stream --format json",
		ValidArgsFunction: complete(0, instancesIn(stateRunning, statePaused)),
		RunE:              runInstanceStatsCommand,
	}

	addOutputFlags(cmd, true)
	cmd.Flags().Bool("no-stream", false, "Show the stats once rather than live")

	return cmd
}

func runInstanceStatsCommand(cmd *cobra.Command, args []string) error {
	noStream, _ := cmd.Flags().GetBool("no-stream")

	client, cleanup, err := newClient(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, stop := signal.NotifyContext(contextOf(cmd), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	stream, err := client.GetInstanceStats(ctx, &dicerdv1.GetInstanceStatsRequest{
		Names:  args,
		Follow: !noStream,
	})
	if err != nil {
		return err
	}

	if noStream {
		batch, err := stream.Recv()
		if err != nil {
			return err
		}
		return render(cmd, &printableInstanceStats{Instances: batch.GetInstances()})
	}

	screen := newScreen(cmd.OutOrStdout())
	defer screen.close()

	for {
		batch, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		// Drawn off screen and written at once, so the screen never shows
		// half a table.
		var frame bytes.Buffer
		if err := renderTo(cmd, &frame, &printableInstanceStats{Instances: batch.GetInstances()}); err != nil {
			return err
		}
		read := batch.GetReadTime().AsTime().Local().Format(time.TimeOnly)
		screen.draw(read+" · Ctrl+C to stop", frame.Bytes())
	}
}
