// Package common holds project-wide utilities shared across the controller's
// packages: a process-wide structured logger and small helpers that read the
// runtime environment.
package common

import (
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	// QSpillControllerRuntimeEnv names the environment variable that
	// selects the runtime mode. Value "prod" (case-insensitive) flips the
	// logger to JSON / Info; anything else (including unset) keeps the dev
	// defaults: console / Debug.
	QSpillControllerRuntimeEnv = "QSPILL_CONTROLLER_ENV"
)

var (
	sharedLogger    logr.Logger
	sharedZapLogger *zap.Logger
	once            sync.Once
)

// InitLogger initialises the process-wide logger exactly once and returns the
// logr.Logger view used by libraries that consume the logr interface (klog,
// controller-runtime, etc.). Subsequent calls return the same instance.
// Should be called from main() before any goroutine reads Logger().
func InitLogger() logr.Logger {
	once.Do(func() {
		sharedZapLogger = RawZapLogger()
		sharedLogger = zapr.NewLogger(sharedZapLogger)
	})
	return sharedLogger
}

// NewLogger returns a per-module logr.Logger derived from the shared root
// logger by attaching name as the module identifier (logr's WithName). Each
// package in this project owns one such logger so log lines carry a stable
// "module" tag operators can grep on. Safe to call before InitLogger;
// InitLogger's sync.Once guarantees the underlying zap logger is created at
// most once regardless of caller order.
func NewLogger(name string) logr.Logger {
	return InitLogger().WithName(name)
}

// Logger returns the shared *zap.Logger. If InitLogger has not run yet,
// Logger returns a freshly built logger so callers never receive nil; this
// keeps unit-test setup and early-init code paths free of order constraints.
func Logger() *zap.Logger {
	if sharedZapLogger == nil {
		return RawZapLogger()
	}
	return sharedZapLogger
}

// RawZapLogger constructs and returns a brand-new *zap.Logger using the
// environment-driven defaults. The shared logger is built from this; tests
// or specialised callers may also use it directly to obtain an independent
// logger that does not affect the singleton.
func RawZapLogger() *zap.Logger {
	logConfig := zap.Config{
		Level:             zap.NewAtomicLevelAt(zap.DebugLevel),
		Development:       true,
		DisableCaller:     false,
		DisableStacktrace: false,
		Encoding:          "console",
		OutputPaths: []string{
			"stderr",
		},
		ErrorOutputPaths: []string{
			"stderr",
		},
	}
	encoderCfg := zap.NewDevelopmentEncoderConfig()
	if IsProdRuntime() {
		logConfig.Development = false
		logConfig.Encoding = "json"
		logConfig.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
		encoderCfg = zap.NewProductionEncoderConfig()
	}
	encoderCfg.TimeKey = "timestamp"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	logConfig.EncoderConfig = encoderCfg
	zapLogger, initLogErr := logConfig.Build()
	if initLogErr != nil {
		panic(fmt.Sprintf("Failed to initialize zap logger %v", initLogErr))
	}
	return zapLogger
}
