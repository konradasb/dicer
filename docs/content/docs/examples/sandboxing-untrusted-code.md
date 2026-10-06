---
title: Sandboxing untrusted code
weight: 2
description: "Run code you don't trust, such as AI-generated scripts, each job in a throwaway machine that cannot reach the network or the host."
icon: shield-check
related:
  - /docs/concepts/networking
  - /docs/guides/snapshots
  - /docs/guides/using-the-api
  - /docs/guides/capacity
---

Code you don't trust, such as a script an AI model wrote or a user
uploaded, is best run in a machine of its own. Here each job gets a fresh
instance, forked from a snapshot of a machine that is already booted, with
its packages installed. The fork is ready in under half a second. It runs
on an internal network, so it cannot reach the outside world, other
networks or the host. When the job is done, the instance is deleted.

## Setup

These steps assume the `default` network and the `linux-6.18` kernel from
the [quickstart](../../getting-started/quickstart).

{{% steps %}}

### Create the sandbox network

```console
$ dicer network create sandbox --subnet 172.30.0.0/24 --internal
Network sandbox created (172.30.0.0/24, gateway 172.30.0.1, bridge dicer-sandbox)
```

An [internal network](../../concepts/networking#internal-networks) keeps its
instances from reaching anything beyond it. The host enforces this, so code
in the guest cannot undo it, even as root.

### Prepare a base machine

Start the machine every job will be a copy of, with what the jobs need.
It runs on the `default` network, so that it can download packages:

```console
$ dicer run -d --name python-base --network default --kernel linux-6.18 \
    --vcpus 1 --memory 512MiB python:3.13-slim sleep infinity
Instance python-base started in 991ms (172.20.71.242)
$ until dicer exec python-base true 2>/dev/null; do sleep 0.2; done
$ dicer exec python-base pip install --quiet --root-user-action=ignore numpy
```

The guest agent that runs `dicer exec` takes a moment to start after the
machine does, so the loop waits for it. Every job gets the base's vCPUs,
memory and disk, so size the base for the largest job.

Now that there are two networks, an instance must name the one it joins,
as the base does with `--network default`. The exception is a host whose
daemon configuration sets
[`defaults.network`](../../reference/configuration#defaults-network).

### Take a snapshot of it

```console
$ dicer snapshot create python-base python-base
Snapshot python-base of instance python-base created in 552ms (memory, 649.1 MiB)
$ dicer rm -f python-base
Instance python-base deleted
```

A memory snapshot holds the running machine: its memory, with Python
already started, and its disk, with numpy installed. The snapshot outlives
the instance, so the base can be deleted, freeing its CPU and memory.

{{% /steps %}}

## Run a job

Here is a script that does some work, and also tries to reach the internet
and the host:

```python {filename="job.py"}
import json, socket, urllib.request
import numpy as np

result = {"mean": float(np.arange(1, 101).mean())}

try:
    urllib.request.urlopen("https://example.com", timeout=3)
    result["internet"] = "reachable"
except OSError as e:
    result["internet"] = f"blocked ({type(e).__name__})"

try:
    socket.create_connection(("host.dicer.internal", 22), timeout=3)
    result["host"] = "reachable"
except OSError as e:
    result["host"] = f"blocked ({type(e).__name__})"

json.dump(result, open("/tmp/result.json", "w"))
print("done")
```

Fork a sandbox onto the `sandbox` network, run the script in it, and copy
out its result:

```console
$ dicer snapshot fork python-base job-1 --network sandbox
Instance job-1 forked from snapshot python-base in 445ms (172.30.0.243)
$ dicer exec -T --timeout 30 job-1 python3 - < job.py
done
$ dicer cp job-1:/tmp/result.json .
Copied job-1:/tmp/result.json to . (2 KiB)
$ cat result.json
{"mean": 50.5, "internet": "blocked (URLError)", "host": "blocked (gaierror)"}
```

`python3 -` reads the script from standard input, so it never has to be
copied into the guest. `-T` passes the script through as it is, without a
terminal. `dicer exec` exits with the script's exit code. `--timeout 30`
kills a script that runs for longer, and the exit code is then 124:

```console
$ echo 'while True: pass' | dicer exec -T --timeout 5 job-1 python3 -
$ echo $?
124
```

When the job is done, delete the sandbox:

```console
$ dicer rm -f job-1
Instance job-1 deleted
```

The fork starts quickly because it doesn't wait for the snapshot's memory to
be read. The guest reads each page of memory when it first uses it. That
needs Cloud Hypervisor v53 or later; see
[How fast a restore is](../../guides/snapshots#how-fast-a-restore-is).

## Run jobs from a program

A service that runs jobs does the same through the [API](../../guides/using-the-api).
This program forks a sandbox, sends it the script on its standard input,
streams the script's output back, and deletes the sandbox:

```go {filename="main.go"}
// Command sandbox runs the Python script on its standard input in a new
// fork of the python-base snapshot, and deletes the fork afterwards. It
// exits with the script's exit code.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/konradasb/dicer"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func main() {
	script, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatal(err)
	}

	client, err := dicer.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	name := fmt.Sprintf("job-%d", time.Now().UnixNano())
	code, err := runJob(context.Background(), client, name, script)
	if err != nil {
		log.Fatal(err)
	}
	os.Exit(code)
}

// runJob forks a sandbox called name, runs script in it with python3, and
// deletes the sandbox, whatever happens.
func runJob(ctx context.Context, client *dicer.Client, name string, script []byte) (int, error) {
	_, err := client.ForkSnapshot(ctx, &dicerdv1.ForkSnapshotRequest{
		Name:        "python-base",
		ForkName:    name,
		NetworkName: "sandbox",
	})
	if err != nil {
		return 0, fmt.Errorf("fork a sandbox: %w", err)
	}
	defer func() {
		_, _ = client.DeleteInstance(context.WithoutCancel(ctx),
			&dicerdv1.DeleteInstanceRequest{Name: name, Force: true})
	}()

	stream, err := client.ExecInstance(ctx)
	if err != nil {
		return 0, err
	}
	err = stream.Send(&dicerdv1.ExecInstanceRequest{
		Payload: &dicerdv1.ExecInstanceRequest_Start{Start: &dicerdv1.ExecInstanceStart{
			Name:           name,
			Command:        []string{"python3", "-"},
			TimeoutSeconds: 30,
		}},
	})
	if err != nil {
		return 0, err
	}
	err = stream.Send(&dicerdv1.ExecInstanceRequest{
		Payload: &dicerdv1.ExecInstanceRequest_Stdin{Stdin: script},
	})
	if err != nil {
		return 0, err
	}
	// Closing our side ends the script's standard input.
	if err := stream.CloseSend(); err != nil {
		return 0, err
	}

	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return 0, errors.New("the stream ended without an exit code")
		}
		if err != nil {
			return 0, err
		}
		switch p := resp.GetPayload().(type) {
		case *dicerdv1.ExecInstanceResponse_Stdout:
			_, _ = os.Stdout.Write(p.Stdout)
		case *dicerdv1.ExecInstanceResponse_Stderr:
			_, _ = os.Stderr.Write(p.Stderr)
		case *dicerdv1.ExecInstanceResponse_ExitCode:
			return int(p.ExitCode), nil
		}
	}
}
```

```console
$ go build -o sandbox .
$ echo 'print(1/0)' | ./sandbox
Traceback (most recent call last):
  File "<stdin>", line 1, in <module>
ZeroDivisionError: division by zero
$ echo $?
1
```

Each job takes about two seconds from start to end, with the fork, the
script and the deletion. Jobs can run side by side, as many as the host has
room for: each sandbox is committed its vCPU and memory while it exists.
See [Capacity](../../guides/capacity).

## What a sandbox can and cannot do

Each sandbox is a virtual machine with a kernel of its own. Code in it runs
as root in the guest, but cannot see the host's processes or files, or
other sandboxes'.

On the `sandbox` network, a sandbox cannot reach:

- the outside world;
- other networks, and the instances on them;
- any service on the host, including Dicer's API;
- upstream nameservers. Only the network's own instances' names resolve.

Sandboxes on the same network can reach each other. To stop that too,
create the network with `--isolated` as well as `--internal`.

A sandbox's use of the host is bounded by what the base was given:

- **CPU and memory:** the base's vCPUs and memory. A job that runs out of
  memory fails inside its own guest.
- **Disk:** the base's overlay disk, 10 GiB unless `--disk` says otherwise.
  Limit how fast it can be read and written with `--disk-rate` and
  `--disk-iops` on the base. See
  [Rate limits](../../guides/running-workloads#rate-limits).
- **Time:** the `--timeout` of each `dicer exec`, or `timeout_seconds` in
  the API.

## Keeping the base current

The snapshot is a picture of the base as it was. To change the packages,
or move to a newer image, prepare a new base and take a new snapshot. A
memory snapshot can be forked only by the
[hypervisor version](../../concepts/hypervisors#before-a-version-is-removed)
that took it, so take a new one before upgrading to a release of Dicer that
removes that version.
