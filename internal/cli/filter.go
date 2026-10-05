// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// instanceFilterKeys are what 'dicer ps --filter' can match on.
var instanceFilterKeys = []string{"name", "state", "image", "network", "label"}

// instanceFilters are the --filter flags of 'dicer ps'. An instance must match
// every key, and any value of a repeated key.
type instanceFilters map[string][]string

// parseInstanceFilters parses KEY=VALUE filters.
func parseInstanceFilters(specs []string) (instanceFilters, error) {
	filters := make(instanceFilters, len(specs))
	for _, s := range specs {
		key, value, ok := strings.Cut(s, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if !ok || value == "" {
			return nil, fmt.Errorf("invalid filter %q: want KEY=VALUE, with KEY one of %s",
				s, strings.Join(instanceFilterKeys, ", "))
		}
		if !slices.Contains(instanceFilterKeys, key) {
			return nil, fmt.Errorf("unknown filter %q: want one of %s", key, strings.Join(instanceFilterKeys, ", "))
		}
		filters[key] = append(filters[key], value)
	}
	return filters, nil
}

// apply returns the instances that match.
func (f instanceFilters) apply(instances []*dicerdv1.Instance) []*dicerdv1.Instance {
	if len(f) == 0 {
		return instances
	}

	matched := make([]*dicerdv1.Instance, 0, len(instances))
	for _, instance := range instances {
		if f.matches(instance) {
			matched = append(matched, instance)
		}
	}
	return matched
}

// matches reports whether an instance matches every key of f.
func (f instanceFilters) matches(instance *dicerdv1.Instance) bool {
	for key, values := range f {
		if !slices.ContainsFunc(values, func(v string) bool { return matchInstance(instance, key, v) }) {
			return false
		}
	}
	return true
}

// matchInstance reports whether instance matches one filter. Names and images
// match on a part, "state=running" regardless of case, a network exactly,
// and a label by key alone or by key and value.
func matchInstance(instance *dicerdv1.Instance, key, value string) bool {
	switch key {
	case "name":
		return strings.Contains(instance.GetName(), value)
	case "state":
		return strings.EqualFold(enumName(instance.GetState()), value)
	case "image":
		return strings.Contains(instance.GetImageRef(), value)
	case "network":
		return instance.GetNetworkName() == value
	case "label":
		k, v, hasValue := strings.Cut(value, "=")
		got, ok := instance.GetLabels()[k]

		return ok && (!hasValue || got == v)
	default:
		return false
	}
}

// completeInstanceFilters completes --filter: a key, then for state, the
// states.
func completeInstanceFilters(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if strings.HasPrefix(toComplete, "state=") {
		states := enumNames[dicerdv1.InstanceState]()
		out := make([]string, 0, len(states))
		for _, s := range states {
			out = append(out, "state="+s)
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}

	out := make([]string, 0, len(instanceFilterKeys))
	for _, k := range instanceFilterKeys {
		out = append(out, k+"=")
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}
