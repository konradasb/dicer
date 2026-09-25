---
title: Using the API
weight: 13
description: "Call the gRPC API from Go, Python or the shell."
icon: code
related:
  - /docs/reference/api
  - /docs/guides/remote-access
  - /docs/reference/events
---

Everything the `dicer` command line does, it does through the daemon's gRPC
API, and so can any program. The API is one service,
`dicerd.v1.DaemonService`, defined in
[`proto/dicerd/v1/dicerd.proto`](https://github.com/konradasb/dicer/blob/main/proto/dicerd/v1/dicerd.proto);
every call and message is listed in the [API reference](../../reference/api).
`dicerd.v1` only grows: calls, messages and fields are added, but never
renamed, renumbered, retyped or removed, so a client built against it keeps
working with newer daemons.

## Connecting

The daemon serves the API on its Unix socket, `unix:///run/dicer/dicer.sock`,
which root and the `dicer` group can open, and optionally on a TCP listener,
with TLS. See
[Remote access](../remote-access) for the listener and its certificates.

## From Go

The `github.com/konradasb/dicer` package makes the connection, and the
generated code in `proto/dicerd/v1` is the API:

```console
$ go get github.com/konradasb/dicer
```

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func main() {
	ctx := context.Background()

	// The local daemon, on its socket.
	c, err := dicer.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	inst, err := c.CreateInstance(ctx, &dicerdv1.CreateInstanceRequest{
		Name:        "web",
		ImageRef:    "nginx:1.27",
		Vcpus:       1,
		MemoryBytes: 512 << 20,
		DiskBytes:   10 << 30,
		Ports: []*dicerdv1.PortMapping{
			{HostPort: 8080, GuestPort: 80, Protocol: dicerdv1.Protocol_PROTOCOL_TCP},
		},
		Start: true,
	})
	switch status.Code(err) {
	case codes.OK:
		fmt.Printf("%s is %s at %s\n", inst.GetName(), inst.GetState(), inst.GetIp())
	case codes.AlreadyExists:
		fmt.Println("web exists already")
	default:
		log.Fatal(status.Convert(err).Message())
	}

	// Follow its console until it stops.
	logs, err := c.GetInstanceLogs(ctx, &dicerdv1.GetInstanceLogsRequest{Name: "web", Follow: true})
	if err != nil {
		log.Fatal(err)
	}
	for {
		chunk, err := logs.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		os.Stdout.Write(chunk.GetData())
	}
}
```

A `dicer.Client` is the generated `DaemonServiceClient`, so its methods are
the API's calls. It is safe for concurrent use; make one and share it.

For a daemon's TCP listener, give its address and a TLS configuration:

```go
cert, err := tls.LoadX509KeyPair("client.pem", "client-key.pem")
// …
roots := x509.NewCertPool()
roots.AppendCertsFromPEM(caPEM)

c, err := dicer.NewClient(
	dicer.WithAddress("dicer1.example.com:7443"),
	dicer.WithTLS(&tls.Config{
		RootCAs:      roots,
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}),
)
```

`dicer.WithDialOptions` adds any other gRPC dial option, such as an
interceptor or keepalives.

## From other languages

Generate a client from the proto file with your language's gRPC tools. It
imports only Google's well-known types, which the tools include. For
Python:

```console
$ pip install grpcio grpcio-tools
$ python -m grpc_tools.protoc -I dicer/proto \
    --python_out=. --grpc_python_out=. dicerd/v1/dicerd.proto
```

```python
import grpc
from dicerd.v1 import dicerd_pb2, dicerd_pb2_grpc

with grpc.insecure_channel("unix:///run/dicer/dicer.sock") as channel:
    daemon = dicerd_pb2_grpc.DaemonServiceStub(channel)

    for inst in daemon.ListInstances(dicerd_pb2.ListInstancesRequest()).instances:
        print(inst.name, dicerd_pb2.InstanceState.Name(inst.state), inst.ip)

    try:
        daemon.GetInstance(dicerd_pb2.GetInstanceRequest(name="db"))
    except grpc.RpcError as e:
        if e.code() == grpc.StatusCode.NOT_FOUND:
            print("no db")
```

`insecure_channel` is right for the socket, which has no TLS; for a TCP
listener with TLS, use `grpc.secure_channel` with
`grpc.ssl_channel_credentials`.

## From the shell

The daemon does not serve reflection, so give
[grpcurl](https://github.com/fullstorydev/grpcurl) the proto file:

```console
$ grpcurl -plaintext -unix \
    -import-path dicer/proto -proto dicerd/v1/dicerd.proto \
    /run/dicer/dicer.sock dicerd.v1.DaemonService/GetHostInfo
$ grpcurl -plaintext -unix \
    -import-path dicer/proto -proto dicerd/v1/dicerd.proto \
    -d '{"name": "web"}' \
    /run/dicer/dicer.sock dicerd.v1.DaemonService/GetInstance
```
