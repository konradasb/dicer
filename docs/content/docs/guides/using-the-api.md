---
title: Using the API
weight: 14
description: "Call the gRPC API from Go, Python or the shell."
icon: code
related:
  - /docs/reference/api
  - /docs/guides/remote-access
  - /docs/reference/events
---

The `dicer` command line does everything through the daemon's gRPC API, and
any program can do the same. The API is one service,
`dicerd.v1.DaemonService`, defined in
[`proto/dicerd/v1/dicerd.proto`](https://github.com/konradasb/dicer/blob/main/proto/dicerd/v1/dicerd.proto).
The [API reference](../../reference/api) lists every call and message.

The API is covered by Dicer's versioning. A patch release never breaks it.
Until 1.0.0, a minor release may, and its release notes say what changed and
what to do about it.

## Connecting

The daemon serves the API on its Unix socket, `unix:///run/dicer/dicer.sock`,
which root and the `dicer` group can open. It can also serve it on a TCP
listener, with TLS. See [Remote access](../remote-access) for the listener
and its certificates.

## Errors

A failed call ends with a gRPC status. Its code says what kind of failure it
was: `NOT_FOUND` for a resource that does not exist, for example, or
`FAILED_PRECONDITION` for one in the wrong state for the call. Its message
says what went wrong, for a person to read. The
[API reference](../../reference/api) lists the codes. Match on the code, not
the message, which may change.

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

	instance, err := c.CreateInstance(ctx, &dicerdv1.CreateInstanceRequest{
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
		fmt.Printf("%s is %s at %s\n", instance.GetName(), instance.GetState(), instance.GetIp())
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

`dicer.WithKeepalive` sets how often the client checks that the daemon is
still there (see [Remote access](../remote-access#connections-that-go-quiet)).
`dicer.WithDialOptions` adds any other gRPC dial option, such as an
interceptor.

## From other languages

Generate a client from the proto file with your language's gRPC tools. The
file imports only Google's well-known types, which the tools include. For
Python, with the repository cloned into `dicer`:

```console
$ git clone https://github.com/konradasb/dicer
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

`insecure_channel` is right for the socket, which has no TLS. For a TCP
listener with TLS, use `grpc.secure_channel` with
`grpc.ssl_channel_credentials`.

## From the shell

The daemon does not serve gRPC reflection, so give
[grpcurl](https://github.com/fullstorydev/grpcurl) the proto file, from the
cloned repository:

```console
$ grpcurl -plaintext -unix \
    -import-path dicer/proto -proto dicerd/v1/dicerd.proto \
    /run/dicer/dicer.sock dicerd.v1.DaemonService/GetHostInfo
$ grpcurl -plaintext -unix \
    -import-path dicer/proto -proto dicerd/v1/dicerd.proto \
    -d '{"name": "web"}' \
    /run/dicer/dicer.sock dicerd.v1.DaemonService/GetInstance
```

For a TCP listener with TLS, replace `-plaintext -unix` and the socket with
`-cacert ca.pem -cert client.pem -key client-key.pem` and the daemon's
`HOST:PORT`.
