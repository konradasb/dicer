// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The convert_*.go files are the client's half of the API's wire format:
// what it sends to the daemon, and what it reads back. The daemon's half --
// reading those requests, writing those replies -- is in internal/grpcapi,
// which is the only other package that knows the generated types exist.
//
// The two halves are inverses rather than copies, so nothing is written
// twice: each direction of each message is converted in exactly one place,
// on the side that needs it. Splitting them this way is what lets the domain
// types and the client live in the same package, which is the one thing a
// caller should have to import.

// timestamp converts a time to a message field, leaving a zero time unset:
// "never" is not a time, and a daemon that reads one back gets a zero time
// again.
func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}

// goTime is timestamp backwards.
func goTime(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}

	return t.AsTime()
}

// duration converts a duration to a message field, leaving a zero one unset,
// as a request that did not set it would.
func duration(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}

	return durationpb.New(d)
}
