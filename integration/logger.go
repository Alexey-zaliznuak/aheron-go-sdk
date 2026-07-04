package integration

import "github.com/Alexey-zaliznuak/aheron-go-sdk/internal/logx"

// Logger is the structured logging seam the SDK writes to. Implement it to route
// SDK logs into your own logging stack, or use github.com/Alexey-zaliznuak/aheron-go-sdk/integration/zaplog
// for a ready-made zap adapter. The default is a no-op (the SDK is silent).
type Logger = logx.Logger

// LogField is a single structured key/value on a log record.
type LogField = logx.Field

// LogF constructs a LogField.
func LogF(key string, value any) LogField { return logx.F(key, value) }

// NopLogger returns a Logger that discards everything (the SDK default).
func NopLogger() Logger { return logx.Nop() }
