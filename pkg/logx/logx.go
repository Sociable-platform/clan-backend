// Package logx builds the structured JSON logger every service uses.
package logx

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a JSON logger that tags every line with the service name.
// level is one of debug, info, warn, error (default info).
func New(service, level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	return slog.New(h).With("service", service)
}
