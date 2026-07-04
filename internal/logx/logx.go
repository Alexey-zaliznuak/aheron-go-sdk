// Package logx defines the minimal structured logging seam used across the SDK.
//
// The SDK never depends on a concrete logging library: callers pass any value
// that satisfies Logger. A no-op implementation (Nop) is the default so the SDK
// is silent unless a logger is provided. A ready-made zap adapter lives in
// github.com/Alexey-zaliznuak/aheron-go-sdk/integration/zaplog for callers that use zap (the convention of
// the rest of the platform).
package logx

// Field is a single structured key/value attached to a log line. Keeping it a
// plain struct (instead of a library-specific field type) is what makes the
// Logger interface dependency-free.
type Field struct {
	Key   string
	Value any
}

// F is a shorthand constructor for a Field.
func F(key string, value any) Field { return Field{Key: key, Value: value} }

// Logger is the structured logging contract the SDK writes to. Implementations
// must be safe for concurrent use.
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
}

type nop struct{}

func (nop) Debug(string, ...Field) {}
func (nop) Info(string, ...Field)  {}
func (nop) Warn(string, ...Field)  {}
func (nop) Error(string, ...Field) {}

// Nop returns a Logger that discards every record. It is the SDK default.
func Nop() Logger { return nop{} }
