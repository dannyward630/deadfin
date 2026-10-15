// SPDX-License-Identifier: GPL-2.0-only

package identify

import (
	"context"
	"log/slog"
)

// SlogLogger adapts a *slog.Logger to the Logger interface so the composition
// root can inject the application logger for provider-error observability.
type SlogLogger struct{ Logger *slog.Logger }

// LogAttrs forwards to the wrapped slog logger, converting the identify
// package's aliased Level and Attr to slog equivalents.
func (l SlogLogger) LogAttrs(ctx context.Context, level Level, msg string, attrs ...Attr) {
	if l.Logger == nil {
		return
	}
	slogAttrs := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		slogAttrs = append(slogAttrs, slog.Any(attr.Key, attr.Value))
	}
	l.Logger.LogAttrs(ctx, slog.Level(level), msg, slogAttrs...)
}
