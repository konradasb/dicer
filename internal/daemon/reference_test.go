// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// configReference is the documentation's page for the configuration, which
// make docs-gen generates from Config's doc comments.
const configReference = "../../docs/content/docs/reference/configuration.md"

// TestConfigReferenceStatesTheDefaults keeps the defaults the configuration
// reference states in step with defaultConfig: every key with a default
// names it in its entry.
func TestConfigReferenceStatesTheDefaults(t *testing.T) {
	page, err := os.ReadFile(configReference)
	if err != nil {
		t.Fatal(err)
	}

	defaults := reflect.ValueOf(defaultConfig())
	for _, key := range configKeys(defaults.Type(), "") {
		want := documentedDefault(lookupField(defaults, strings.Split(key, ".")))
		if want == "" {
			continue
		}

		entry, ok := referenceEntry(string(page), key)
		if !ok {
			t.Errorf("the configuration reference has no entry for %s: run make docs-gen", key)
			continue
		}
		if !strings.Contains(entry, "Unset is "+want) && !strings.Contains(entry, "Unset is `"+want+"`") {
			t.Errorf("the configuration reference does not say %s is %s when unset:\n%s", key, want, entry)
		}
	}
}

// referenceEntry returns the text of a key's entry on the reference page.
func referenceEntry(page, key string) (string, bool) {
	_, entry, ok := strings.Cut(page, "\n### `"+key+"` {#")
	if !ok {
		return "", false
	}
	entry, _, _ = strings.Cut(entry, "\n#")

	return entry, true
}

// configKeys returns the dotted YAML path of every leaf key in t.
func configKeys(t reflect.Type, prefix string) []string {
	var keys []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		if f.Type.Kind() == reflect.Struct && f.Type.PkgPath() == t.PkgPath() {
			keys = append(keys, configKeys(f.Type, prefix+name+".")...)
			continue
		}
		keys = append(keys, prefix+name)
	}
	return keys
}

// lookupField returns the field of v at a dotted YAML path.
func lookupField(v reflect.Value, path []string) reflect.Value {
	for _, name := range path {
		t := v.Type()
		for i := range t.NumField() {
			if key, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ","); key == name {
				v = v.Field(i)
				break
			}
		}
	}
	return v
}

// documentedDefault returns a default as the reference writes it, or "" for
// one that is the zero value, which the reference describes in words.
func documentedDefault(v reflect.Value) string {
	if v.IsZero() {
		return ""
	}

	switch x := v.Interface().(type) {
	case time.Duration:
		s := x.String()
		s = strings.TrimSuffix(s, "0s")
		return strings.TrimSuffix(s, "0m")
	case uint32:
		return fmt.Sprintf("%#o", x)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}
