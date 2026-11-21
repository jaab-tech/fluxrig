// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package snake

import (
	"context"
	"fmt"
	"log/slog"
)

// natsLogger carries what the embedded NATS server reports into slog. Without
// it the server logs nothing: a route refused for its TLS certificate, a
// route that drops, or a JetStream warning reaches no operator.
type natsLogger struct {
	log func() *slog.Logger
}

// newNATSLogger returns a logger that reads slog.Default at each call, so a
// handler installed after the server starts still receives its messages.
func newNATSLogger() *natsLogger {
	return &natsLogger{log: slog.Default}
}

func (l *natsLogger) emit(level slog.Level, format string, v ...any) {
	lg := l.log()
	if !lg.Enabled(context.Background(), level) {
		return
	}
	lg.Log(context.Background(), level, fmt.Sprintf(format, v...), "flux.component", "snake.nats")
}

// Noticef logs the server's notices, such as a route that connects or closes.
func (l *natsLogger) Noticef(format string, v ...any) { l.emit(slog.LevelInfo, format, v...) }

// Warnf logs the server's warnings.
func (l *natsLogger) Warnf(format string, v ...any) { l.emit(slog.LevelWarn, format, v...) }

// Errorf logs the server's errors, such as a refused TLS handshake.
func (l *natsLogger) Errorf(format string, v ...any) { l.emit(slog.LevelError, format, v...) }

// Fatalf logs at error level. The embedded server reports its own failure to
// the caller; the process must not exit from inside a log call.
func (l *natsLogger) Fatalf(format string, v ...any) { l.emit(slog.LevelError, format, v...) }

// Debugf logs the server's debug output. The server sends it only when the
// snake log level is debug or trace.
func (l *natsLogger) Debugf(format string, v ...any) { l.emit(slog.LevelDebug, format, v...) }

// Tracef logs the server's protocol trace, sent only at the trace level.
func (l *natsLogger) Tracef(format string, v ...any) { l.emit(slog.LevelDebug, format, v...) }
