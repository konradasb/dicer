// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcserver

import (
	"context"
	"path"

	"google.golang.org/grpc"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/token"
)

// methodScopes are the scope each call needs, by method. Every method of the
// API is here: one missing is refused to every token, so that a call added
// to the API is not open by mistake. The zero Scope lets any token make the
// call.
//
// A call needs the scope of the resource it is about. Exec and copying go
// into the guest, so they need instances:write either way, and a call that
// makes or changes an instance from a snapshot needs instances:write too.
// CheckHost needs instances:write only to boot a test instance, which its
// handler checks.
var methodScopes = map[string]token.Scope{
	"CreateInstance":         token.ScopeInstancesWrite,
	"UpdateInstance":         token.ScopeInstancesWrite,
	"RenameInstance":         token.ScopeInstancesWrite,
	"StartInstance":          token.ScopeInstancesWrite,
	"StopInstance":           token.ScopeInstancesWrite,
	"PauseInstance":          token.ScopeInstancesWrite,
	"ResumeInstance":         token.ScopeInstancesWrite,
	"StandbyInstance":        token.ScopeInstancesWrite,
	"ForkInstance":           token.ScopeInstancesWrite,
	"ResizeInstance":         token.ScopeInstancesWrite,
	"DeleteInstance":         token.ScopeInstancesWrite,
	"ListInstances":          token.ScopeInstancesRead,
	"GetInstance":            token.ScopeInstancesRead,
	"WaitInstance":           token.ScopeInstancesRead,
	"GetInstanceLogs":        token.ScopeInstancesRead,
	"GetInstanceStats":       token.ScopeInstancesRead,
	"ListInstanceProcesses":  token.ScopeInstancesRead,
	"ExecInstance":           token.ScopeInstancesWrite,
	"CopyToInstance":         token.ScopeInstancesWrite,
	"CopyFromInstance":       token.ScopeInstancesWrite,
	"CreateSnapshot":         token.ScopeSnapshotsWrite,
	"ListSnapshots":          token.ScopeSnapshotsRead,
	"GetSnapshot":            token.ScopeSnapshotsRead,
	"DeleteSnapshot":         token.ScopeSnapshotsWrite,
	"RestoreSnapshot":        token.ScopeInstancesWrite,
	"ForkSnapshot":           token.ScopeInstancesWrite,
	"CreateNetwork":          token.ScopeNetworksWrite,
	"ListNetworks":           token.ScopeNetworksRead,
	"GetNetwork":             token.ScopeNetworksRead,
	"DeleteNetwork":          token.ScopeNetworksWrite,
	"ListNetworkAllocations": token.ScopeNetworksRead,
	"CreateVolume":           token.ScopeVolumesWrite,
	"ListVolumes":            token.ScopeVolumesRead,
	"GetVolume":              token.ScopeVolumesRead,
	"DeleteVolume":           token.ScopeVolumesWrite,
	"PullImage":              token.ScopeImagesWrite,
	"ListImages":             token.ScopeImagesRead,
	"GetImage":               token.ScopeImagesRead,
	"DeleteImage":            token.ScopeImagesWrite,
	"PruneImages":            token.ScopeImagesWrite,
	"ImportKernel":           token.ScopeKernelsWrite,
	"ListKernels":            token.ScopeKernelsRead,
	"GetKernel":              token.ScopeKernelsRead,
	"DeleteKernel":           token.ScopeKernelsWrite,
	"CreateToken":            token.ScopeTokensWrite,
	"ListTokens":             token.ScopeTokensRead,
	"GetToken":               token.ScopeTokensRead,
	"RotateToken":            token.ScopeTokensWrite,
	"DeleteToken":            token.ScopeTokensWrite,
	"GetHostInfo":            "",
	"CheckHost":              "",
	"GetResources":           token.ScopeInstancesRead,
	"GetEvents":              token.ScopeEventsRead,
}

// unaryAuthorizationInterceptor refuses a unary call its token's scopes do
// not allow. It belongs after authentication, which puts the token in the
// call's context: a call without one is refused.
func unaryAuthorizationInterceptor(
	ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
) (any, error) {
	if err := authorize(ctx, info.FullMethod); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// streamAuthorizationInterceptor refuses a streaming call its token's scopes
// do not allow, as unaryAuthorizationInterceptor does.
func streamAuthorizationInterceptor(
	srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler,
) error {
	if err := authorize(ss.Context(), info.FullMethod); err != nil {
		return err
	}
	return handler(srv, ss)
}

// authorize returns an ErrPermissionDenied error unless the call's token
// has the scope its method needs.
func authorize(ctx context.Context, fullMethod string) error {
	method := path.Base(fullMethod)
	scope, known := methodScopes[method]
	t, ok := tokenFrom(ctx)

	switch {
	case !ok:
		return errdefs.PermissionDenied("%s needs a token", method)
	case !known:
		return errdefs.PermissionDenied("%s is not open to tokens", method)
	case scope != "" && !t.Allows(scope):
		return errdefs.PermissionDenied("token %q lacks scope %s", t.Name, scope)
	}
	return nil
}
