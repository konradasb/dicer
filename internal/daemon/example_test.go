// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const exampleConfig = "../../example.yml"

// TestExampleConfigIsTheDefaults keeps example.yml loadable and in step with
// defaultConfig.
func TestExampleConfigIsTheDefaults(t *testing.T) {
	cfg, err := loadConfig(exampleConfig)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if want := defaultConfig(); !reflect.DeepEqual(*cfg, want) {
		t.Errorf("example.yml = %+v\nwant the defaults %+v", *cfg, want)
	}
}

// TestExampleConfigShowsEveryKey keeps example.yml complete.
func TestExampleConfigShowsEveryKey(t *testing.T) {
	data, err := os.ReadFile(exampleConfig)
	if err != nil {
		t.Fatal(err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}

	for _, key := range configKeys(reflect.TypeFor[Config](), "") {
		if lookupKey(doc.Content[0], strings.Split(key, ".")) == nil {
			t.Errorf("example.yml does not show %s", key)
		}
	}
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

// lookupKey returns the value at path in a mapping node, or nil.
func lookupKey(n *yaml.Node, path []string) *yaml.Node {
	if len(path) == 0 {
		return n
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == path[0] {
			return lookupKey(n.Content[i+1], path[1:])
		}
	}
	return nil
}
