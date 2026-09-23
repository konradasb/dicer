// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
)

// instanceFilterKeys are what 'dicer ps --filter' can match on.
var instanceFilterKeys = []string{"name", "state", "image", "network", "label"}

// instanceFilters are the --filter flags of 'dicer ps'. An instance is shown
// if it matches every key given, and for a key given more than once, any of
// its values -- as with docker ps.
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
func (f instanceFilters) apply(instances []dicer.Instance) []dicer.Instance {
	if len(f) == 0 {
		return instances
	}

	matched := make([]dicer.Instance, 0, len(instances))
	for _, inst := range instances {
		if f.matches(inst) {
			matched = append(matched, inst)
		}
	}
	return matched
}

// matches reports whether an instance matches every key of f.
func (f instanceFilters) matches(inst dicer.Instance) bool {
	for key, values := range f {
		if !slices.ContainsFunc(values, func(v string) bool { return matchInstance(inst, key, v) }) {
			return false
		}
	}
	return true
}

// matchInstance reports whether inst matches one filter. Names and images
// match on a part, "state=running" regardless of case, a network exactly,
// and a label by key alone or by key and value.
func matchInstance(inst dicer.Instance, key, value string) bool {
	switch key {
	case "name":
		return strings.Contains(inst.Spec.Name, value)
	case "state":
		return strings.EqualFold(inst.Status.State.String(), value)
	case "image":
		return strings.Contains(inst.Spec.ImageRef, value)
	case "network":
		return inst.Spec.NetworkName == value
	case "label":
		k, v, hasValue := strings.Cut(value, "=")
		got, ok := inst.Spec.Labels[k]

		return ok && (!hasValue || got == v)
	default:
		return false
	}
}

// completeInstanceFilters completes --filter: a key, then for state, the
// states.
func completeInstanceFilters(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if strings.HasPrefix(toComplete, "state=") {
		states := dicer.InstanceStates()
		out := make([]string, 0, len(states))
		for _, s := range states {
			out = append(out, "state="+s.Lower())
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}

	out := make([]string, 0, len(instanceFilterKeys))
	for _, k := range instanceFilterKeys {
		out = append(out, k+"=")
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}
