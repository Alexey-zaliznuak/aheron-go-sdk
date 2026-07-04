// Package zaplog adapts a *zap.Logger to the integration SDK's Logger interface.
// Import it only if you use zap (the logging convention of the rest of the
// platform); the core SDK has no zap dependency of its own beyond this package.
//
//	logger, _ := zap.NewProduction()
//	c, _ := integration.New(integration.Config{
//	    IntegrationID: id,
//	    PrivateKey:    key,
//	    Logger:        zaplog.New(logger),
//	})
package zaplog

import (
	"github.com/Alexey-zaliznuak/aheron-go-sdk/integration"

	"go.uber.org/zap"
)

type adapter struct {
	l *zap.Logger
}

// New wraps a *zap.Logger as an integration.Logger. A nil logger yields the
// SDK's no-op logger.
func New(l *zap.Logger) integration.Logger {
	if l == nil {
		return integration.NopLogger()
	}
	return adapter{l: l}
}

func fields(in []integration.LogField) []zap.Field {
	out := make([]zap.Field, 0, len(in))
	for _, f := range in {
		out = append(out, zap.Any(f.Key, f.Value))
	}
	return out
}

func (a adapter) Debug(msg string, f ...integration.LogField) { a.l.Debug(msg, fields(f)...) }
func (a adapter) Info(msg string, f ...integration.LogField)  { a.l.Info(msg, fields(f)...) }
func (a adapter) Warn(msg string, f ...integration.LogField)  { a.l.Warn(msg, fields(f)...) }
func (a adapter) Error(msg string, f ...integration.LogField) { a.l.Error(msg, fields(f)...) }
