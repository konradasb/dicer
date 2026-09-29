// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/konradasb/dicer/internal/metrics"
)

// metricSection is a section of the metrics reference: a group's table, and
// what follows it.
type metricSection struct {
	group, heading, after string
}

// metricSections are the reference's sections, in order. A metric in a group
// with no section fails the generation.
var metricSections = []metricSection{
	{
		group:   metrics.GroupDaemon,
		heading: "Daemon",
	},
	{
		group:   metrics.GroupInstances,
		heading: "Instances",
		after: "The allocatable amounts are the host's CPUs and memory, less the reserve, multiplied by " +
			"the overcommit, as the configuration's [`resources`]({{< relref \"/docs/reference/configuration#resources\" >}}) " +
			"sets them. See [Capacity]({{< relref \"/docs/guides/capacity\" >}}).",
	},
	{
		group:   metrics.GroupImages,
		heading: "Images",
	},
	{
		group:   metrics.GroupNetworks,
		heading: "Networks",
	},
	{
		group:   metrics.GroupAPI,
		heading: "API",
	},
}

// metricsIntro opens the page.
const metricsIntro = "The Prometheus metrics the daemon serves at `/metrics`, once " +
	"[enabled]({{< relref \"/docs/reference/configuration#metrics\" >}}) with `metrics.enable`. " +
	"Every metric of Dicer's own is named `dicer_`. See " +
	"[Monitoring]({{< relref \"/docs/guides/monitoring\" >}}) for scraping and alerts.\n\n" +
	"The endpoint also serves the Go runtime's `go_*` metrics and the daemon process's `process_*` " +
	"metrics, and speaks OpenMetrics to a scraper that asks for it.\n"

// writeMetrics writes the metrics reference from those the daemon registers.
func writeMetrics(dir string) error {
	// Every source given, so that the gauges read from them are registered
	// too; they are never read here.
	m := metrics.New(metrics.Options{Sources: metrics.Sources{
		Instances: func() metrics.InstanceStats { return metrics.InstanceStats{} },
		Networks:  func() []metrics.NetworkStats { return nil },
		Images:    func() metrics.ImageStats { return metrics.ImageStats{} },
	}})

	byGroup := map[string][]metrics.Description{}
	for _, d := range m.Reference() {
		byGroup[d.Group] = append(byGroup[d.Group], d)
	}

	var body bytes.Buffer
	body.WriteString(metricsIntro)

	for _, sec := range metricSections {
		fmt.Fprintf(&body, "\n## %s\n\n", sec.heading)
		body.WriteString("| Metric | Type | Labels | Description |\n|---|---|---|---|\n")
		for _, d := range byGroup[sec.group] {
			labels := make([]string, len(d.Labels))
			for i, l := range d.Labels {
				labels[i] = "`" + l + "`"
			}

			text := d.Help
			if d.Doc != "" {
				text += " " + d.Doc
			}

			fmt.Fprintf(&body, "| `%s` | %s | %s | %s |\n", d.Name, d.Type, strings.Join(labels, ", "), cell(text))
		}

		if sec.after != "" {
			body.WriteString("\n" + sec.after + "\n")
		}

		delete(byGroup, sec.group)
	}

	for group, ds := range byGroup {
		return fmt.Errorf("%s is in group %q, which the reference has no section for", ds[0].Name, group)
	}

	return writePage(filepath.Join(dir, "metrics.md"), meta{
		title: "Metrics", weight: 5, icon: "chart-bar",
		description: "Every Prometheus metric the daemon serves, with its labels.",
	}, body.Bytes())
}
