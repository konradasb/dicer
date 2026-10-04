// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// printableProcesses lists the processes in an instance's guest, a row each.
// Its column names are also a --format template's fields and JSON's keys.
type printableProcesses struct {
	Processes []*dicerdv1.Process
}

func (p *printableProcesses) Cols() []string {
	return []string{"PID", "PPID", "User", "State", "Started", "CPUTime", "RSS", "Command"}
}

func (p *printableProcesses) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Processes))
	for _, process := range p.Processes {
		// A process without a command line, such as a zombie, is shown by
		// its name in brackets.
		command := strings.Join(process.GetCommand(), " ")
		if command == "" {
			command = "[" + process.GetName() + "]"
		}
		kv = append(kv, map[string]any{
			"PID":     strconv.Itoa(int(process.GetPid())),
			"PPID":    strconv.Itoa(int(process.GetPpid())),
			"User":    process.GetUser(),
			"State":   process.GetState(),
			"Started": age(process.GetStartTime().AsTime()),
			"CPUTime": process.GetCpuTime().AsDuration().Round(10 * time.Millisecond).String(),
			"RSS":     size(process.GetResidentMemoryBytes()),
			"Command": command,
		})
	}
	return kv
}

func newInstanceTopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "top NAME",
		Short: "List the processes running in an instance",
		Long: "Lists the processes running in a running instance's guest, as its guest\n" +
			"agent reads them from the guest's /proc. Kernel threads are left out;\n" +
			"dicer-init and the agent are shown, since they run in the guest too.\n\n" +
			"PIDs are the guest's, the ones a command run with dicer exec sees. State is\n" +
			"the kernel's code for the process: R running, S sleeping, D waiting on I/O,\n" +
			"Z zombie, T stopped. CPUTime is the CPU time used since the process started,\n" +
			"and RSS the guest memory it has resident.\n\n" +
			"The column names are also the fields of a --format template, and the keys of\n" +
			"--format json.",
		Example: "  dicer top web\n" +
			"  dicer top web --format '{{.PID}}\\t{{.Command}}'\n" +
			"  dicer top web --format json",
		Args:              one("an instance name"),
		ValidArgsFunction: complete(1, instancesIn(stateRunning)),
		RunE:              runInstanceTopCommand,
	}

	addOutputFlags(cmd, true)

	return cmd
}

func runInstanceTopCommand(cmd *cobra.Command, args []string) error {
	client, cleanup, err := newClient(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	resp, err := client.ListInstanceProcesses(contextOf(cmd), &dicerdv1.ListInstanceProcessesRequest{Name: args[0]})
	if err != nil {
		return err
	}
	return render(cmd, &printableProcesses{Processes: resp.GetProcesses()})
}
