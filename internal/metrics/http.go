// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Handler serves this registry's metrics in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		ErrorLog:          scrapeErrorLog{logger: m.logger},
		ErrorHandling:     promhttp.ContinueOnError,
		EnableOpenMetrics: true,
	})
}

// scrapeErrorLog adapts this daemon's logger to the one promhttp expects.
type scrapeErrorLog struct {
	logger *slog.Logger
}

func (l scrapeErrorLog) Println(v ...any) {
	l.logger.Error("serving scrape", "error", fmt.Sprint(v...))
}
