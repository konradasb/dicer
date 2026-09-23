// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// NewClientWithResponsesFromSocket returns an API client bound to a VMM's
// Unix socket.
func NewClientWithResponsesFromSocket(socketPath string) (ClientWithResponsesInterface, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer

			return d.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	client, err := NewClientWithResponses("http://localhost/api/v1",
		WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	return client, nil
}
