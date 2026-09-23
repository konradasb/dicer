// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package registry

import (
	"log/slog"

	"github.com/apex/log"
)

// umociLogHandler bridges apex/log (used globally by umoci) to slog.
type umociLogHandler struct {
	logger *slog.Logger
}

// HandleLog implements log.Handler, mapping each apex/log level onto its
// slog counterpart. The slog handler decides what is shown.
func (h *umociLogHandler) HandleLog(e *log.Entry) error {
	switch e.Level {
	case log.DebugLevel:
		h.logger.Debug(e.Message)
	case log.InfoLevel:
		h.logger.Info(e.Message)
	case log.WarnLevel:
		h.logger.Warn(e.Message)
	case log.ErrorLevel, log.FatalLevel, log.InvalidLevel:
		h.logger.Error(e.Message)
	}

	return nil
}

// setUmociLogger redirects umoci's global apex/log output to the given slog logger.
func setUmociLogger(logger *slog.Logger) {
	log.SetHandler(&umociLogHandler{logger: logger})
}
