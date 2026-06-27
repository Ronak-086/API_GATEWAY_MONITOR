package logger

import (
	"sync"

	"go.uber.org/zap"
)

// Log is the globally accessible, thread-safe zap logger instance.
// It must be initialized via InitLogger before use.
var Log *zap.Logger

var mu sync.Mutex

// InitLogger initializes the global Log instance based on the provided
// runtime environment. "production" yields a JSON-structured, performance
// optimized logger; any other value yields a human-readable development
// logger with debug-level verbosity.
func InitLogger(env string) error {
	mu.Lock()
	defer mu.Unlock()

	var l *zap.Logger
	var err error

	switch env {
	case "production", "prod":
		l, err = zap.NewProduction()
	default:
		l, err = zap.NewDevelopment()
	}

	if err != nil {
		return err
	}

	Log = l
	return nil
}

// Sync flushes any buffered log entries. It must be called before process
// exit (typically via defer in main) to avoid losing in-flight log writes.
func Sync() {
	mu.Lock()
	defer mu.Unlock()

	if Log == nil {
		return
	}
	_ = Log.Sync()
}