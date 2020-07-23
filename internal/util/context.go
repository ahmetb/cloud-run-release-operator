package util

import (
	"context"

	"github.com/sirupsen/logrus"
)

type contextKeyLogger struct{}

// The logger context key
var loggerKey contextKeyLogger

// ContextWithLogger returns a copy of the parent context that includes the
// logger.
func ContextWithLogger(ctx context.Context, logger *logrus.Entry) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// LoggerFromContext returns the logger from the context.
func LoggerFromContext(ctx context.Context) *logrus.Entry {
	logger := ctx.Value(loggerKey).(*logrus.Entry)
	if logger == nil {
		logger = logrus.NewEntry(logrus.New())
	}

	return logger
}