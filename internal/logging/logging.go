// Package logging configures the application's structured-logging
// handler. JSON to stdout per ADR-006 ("minimum viable observability").
package logging

import (
	"log/slog"
	"os"
)

// New returns a JSON slog.Logger writing to stdout at the given level.
// The returned logger is also installed as slog.Default so libraries
// that read from the default handler pick it up.
func New(level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})
	logger := slog.New(h).With(
		slog.String("service", "geonovum-sta-translation"),
	)
	slog.SetDefault(logger)
	return logger
}
