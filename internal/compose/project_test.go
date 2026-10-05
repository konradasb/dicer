// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package compose

import (
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func TestOrder(t *testing.T) {
	p := mustLoad(t, `
services:
  web: {image: x, depends_on: [api, cache]}
  api: {image: x, depends_on: {db: {condition: service_healthy}}}
  db: {image: x}
  cache: {image: x}
  worker: {image: x, depends_on: [db]}
`, nil)

	names := func(services []*Service) []string {
		out := make([]string, 0, len(services))
		for _, s := range services {
			out = append(out, s.Name)
		}
		return out
	}

	all, err := p.Order()
	if err != nil {
		t.Fatal(err)
	}
	got := names(all)
	if len(got) != 5 {
		t.Fatalf("Order() = %q, want every service", got)
	}
	before := func(a, b string) bool { return slices.Index(got, a) < slices.Index(got, b) }
	for _, pair := range [][2]string{{"db", "api"}, {"api", "web"}, {"cache", "web"}, {"db", "worker"}} {
		if !before(pair[0], pair[1]) {
			t.Errorf("Order() = %q: %s should come before %s", got, pair[0], pair[1])
		}
	}

	// Naming a service brings what it depends on with it.
	web, err := p.Order("web")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(web); !slices.Equal(got, []string{"db", "api", "cache", "web"}) {
		t.Errorf("Order(web) = %q, want web and everything it depends on", got)
	}

	// Selecting does not.
	selected, err := p.Select("web", "db")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(selected); !slices.Equal(got, []string{"db", "web"}) {
		t.Errorf("Select(web, db) = %q, want just those, in order", got)
	}

	if _, err := p.Order("nope"); err == nil {
		t.Error("Order of an unknown service succeeded")
	}

	if dep := p.Services["api"].DependsOn; len(dep) != 1 || dep[0].Condition != ConditionHealthy {
		t.Errorf("api depends on %+v, want db, healthy", dep)
	}
	if dep := p.Services["web"].DependsOn; dep[0].Condition != ConditionStarted {
		t.Errorf("a listed dependency's condition = %q, want started", dep[0].Condition)
	}
}

func TestConfigHash(t *testing.T) {
	compose := "services: {web: {image: 'nginx:${TAG}', environment: {A: '1', B: '2'}}}"
	hash := func(tag string) string {
		p := mustLoad(t, compose, map[string]string{"TAG": tag})
		return p.Services["web"].Instance.GetLabels()[LabelConfigHash]
	}

	if first, again := hash("1"), hash("1"); first != again {
		t.Error("the same definition hashes differently")
	}
	if hash("1") == hash("2") {
		t.Error("a changed image hashes the same")
	}

	p := mustLoad(t, compose, map[string]string{"TAG": "1"})
	req := p.Services["web"].Instance
	started, ok := proto.Clone(req).(*dicerdv1.CreateInstanceRequest)
	if !ok {
		t.Fatal("clone of a CreateInstanceRequest is not one")
	}
	started.Start = true
	if ConfigHash(started) != req.GetLabels()[LabelConfigHash] {
		t.Error("starting the instance changes its hash")
	}
}

func TestServiceFor(t *testing.T) {
	p := mustLoad(t, "services: {web: {image: nginx}}", nil)

	ours := &dicerdv1.Instance{Labels: map[string]string{LabelProject: "shop", LabelService: "web"}}
	if s, ok := p.ServiceFor(ours); !ok || s.Name != "web" {
		t.Errorf("ServiceFor(ours) = %v, %v", s, ok)
	}

	for _, labels := range []map[string]string{
		{LabelProject: "other", LabelService: "web"},
		{LabelProject: "shop", LabelService: "gone"},
		nil,
	} {
		if _, ok := p.ServiceFor(&dicerdv1.Instance{Labels: labels}); ok {
			t.Errorf("ServiceFor(%v) found a service", labels)
		}
	}
}
