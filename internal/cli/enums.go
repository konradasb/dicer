// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// protoEnum is a generated API enum, such as dicerdv1.RestartMode.
type protoEnum interface {
	~int32
	protoreflect.Enum
}

// enumName is how the CLI writes an API enum value: its name without the
// enum's prefix, lower case, with hyphens. RESTART_MODE_ON_FAILURE is
// "on-failure". Unspecified is "".
func enumName[E protoEnum](v E) string {
	if v == 0 {
		return ""
	}

	d := v.Descriptor()
	value := d.Values().ByNumber(v.Number())
	if value == nil {
		return strconv.Itoa(int(v))
	}

	name := strings.TrimPrefix(string(value.Name()), enumPrefix(d))
	return strings.ReplaceAll(strings.ToLower(name), "_", "-")
}

// parseEnum reads a value written as enumName writes it.
func parseEnum[E protoEnum](what, s string) (E, error) {
	var zero E
	d := zero.Descriptor()

	name := enumPrefix(d) + strings.ToUpper(strings.ReplaceAll(s, "-", "_"))
	if value := d.Values().ByName(protoreflect.Name(name)); value != nil && value.Number() != 0 {
		return E(value.Number()), nil
	}

	return zero, fmt.Errorf("invalid %s %q: want %s", what, s, orList(enumNames[E]()))
}

// enumNames returns every value of an enum but unspecified, as enumName
// writes them.
func enumNames[E protoEnum]() []string {
	var zero E
	values := zero.Descriptor().Values()

	names := make([]string, 0, values.Len()-1)
	for i := range values.Len() {
		if n := values.Get(i).Number(); n != 0 {
			names = append(names, enumName(E(n)))
		}
	}
	return names
}

// enumPrefix is what every value of an enum is named with: HYPERVISOR_TYPE_,
// from HYPERVISOR_TYPE_UNSPECIFIED.
func enumPrefix(d protoreflect.EnumDescriptor) string {
	return strings.TrimSuffix(string(d.Values().ByNumber(0).Name()), "UNSPECIFIED")
}

// The API's enum values the CLI refers to, named as it would write them.
const (
	stateStopped    = dicerdv1.InstanceState_INSTANCE_STATE_STOPPED
	stateStarting   = dicerdv1.InstanceState_INSTANCE_STATE_STARTING
	stateRunning    = dicerdv1.InstanceState_INSTANCE_STATE_RUNNING
	statePaused     = dicerdv1.InstanceState_INSTANCE_STATE_PAUSED
	stateStopping   = dicerdv1.InstanceState_INSTANCE_STATE_STOPPING
	stateRestarting = dicerdv1.InstanceState_INSTANCE_STATE_RESTARTING
	stateFailed     = dicerdv1.InstanceState_INSTANCE_STATE_FAILED

	healthStarting  = dicerdv1.HealthStatus_HEALTH_STATUS_STARTING
	healthHealthy   = dicerdv1.HealthStatus_HEALTH_STATUS_HEALTHY
	healthUnhealthy = dicerdv1.HealthStatus_HEALTH_STATUS_UNHEALTHY
)

// stateName is an instance state as the CLI shows it on its own: "Running".
func stateName(s dicerdv1.InstanceState) string {
	return capitalize(enumName(s))
}

// isActive reports whether an instance in state s has a live guest.
func isActive(s dicerdv1.InstanceState) bool {
	return s == stateRunning || s == statePaused
}

// archName is a kernel's architecture as uname -m names it: "x86_64".
func archName(a dicerdv1.Architecture) string {
	return strings.ReplaceAll(enumName(a), "-", "_")
}

// protocolName is a port mapping's protocol: "tcp" when unspecified, as
// the daemon takes it.
func protocolName(p dicerdv1.Protocol) string {
	if p == dicerdv1.Protocol_PROTOCOL_UNSPECIFIED {
		return enumName(dicerdv1.Protocol_PROTOCOL_TCP)
	}
	return enumName(p)
}

// restartPolicyName is a restart policy as --restart takes it:
// "on-failure:5", or "no" for none.
func restartPolicyName(p *dicerdv1.RestartPolicy) string {
	mode := p.GetMode()
	if mode == dicerdv1.RestartMode_RESTART_MODE_UNSPECIFIED {
		mode = dicerdv1.RestartMode_RESTART_MODE_NO
	}
	if n := p.GetMaxRetries(); n > 0 {
		return fmt.Sprintf("%s:%d", enumName(mode), n)
	}
	return enumName(mode)
}
