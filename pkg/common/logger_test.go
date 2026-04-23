package common_test

import (
	"testing"

	"go.uber.org/zap"

	"github.com/pzhenzhou/qspill-controller/pkg/common"
)

// TestLoggerSafeBeforeInit asserts the documented contract that Logger()
// never returns nil even when InitLogger has not been called yet — it falls
// back to a fresh RawZapLogger. This is the "define errors out of existence"
// guarantee callers depend on.
func TestLoggerSafeBeforeInit(t *testing.T) {
	if got := common.Logger(); got == nil {
		t.Fatal("Logger() returned nil before InitLogger; the no-init fallback path is broken")
	}
}

// TestInitLoggerReturnsLogr verifies the InitLogger handshake used by
// klog.SetLogger / controller-runtime style consumers. It also confirms the
// shared zap logger is wired up so subsequent Logger() calls return the
// same singleton.
func TestInitLoggerReturnsLogr(t *testing.T) {
	first := common.InitLogger()
	if first.GetSink() == nil {
		t.Fatal("InitLogger returned a logr.Logger with a nil sink")
	}

	a := common.Logger()
	b := common.Logger()
	if a == nil || b == nil {
		t.Fatal("Logger() returned nil after InitLogger")
	}
	if a != b {
		t.Fatal("Logger() did not return the same singleton across calls after InitLogger")
	}
}

// TestRawZapLoggerDevDefault checks the dev-mode build path: when the env
// var is unset, RawZapLogger returns a console-encoded debug logger that is
// safe to write through. We exercise a real log call to catch encoder
// misconfigurations that only surface at write time.
func TestRawZapLoggerDevDefault(t *testing.T) {
	t.Setenv(common.QSpillControllerRuntimeEnv, "")

	lg := common.RawZapLogger()
	if lg == nil {
		t.Fatal("RawZapLogger returned nil in dev mode")
	}
	lg.Debug("dev-mode-smoke", zap.String("scope", "logger_test"))
	_ = lg.Sync()
}

// TestRawZapLoggerProdMode covers the prod-mode build path: env var set to
// "prod" must produce a JSON-encoded info-level logger without panicking.
// As above, we drive a live log call to surface encoder problems.
func TestRawZapLoggerProdMode(t *testing.T) {
	t.Setenv(common.QSpillControllerRuntimeEnv, "prod")

	lg := common.RawZapLogger()
	if lg == nil {
		t.Fatal("RawZapLogger returned nil in prod mode")
	}
	lg.Info("prod-mode-smoke", zap.String("scope", "logger_test"))
	_ = lg.Sync()
}

// TestIsProdRuntime exercises the env-var contract that drives the prod
// branch in RawZapLogger: the toggle is case-insensitive and only the exact
// value "prod" flips the mode.
func TestIsProdRuntime(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want bool
	}{
		{"empty_value_means_dev", "", false},
		{"prod_lowercase", "prod", true},
		{"prod_uppercase", "PROD", true},
		{"prod_mixed_case", "Prod", true},
		{"dev_explicit", "dev", false},
		{"unrelated_value", "staging", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(common.QSpillControllerRuntimeEnv, tc.val)
			if got := common.IsProdRuntime(); got != tc.want {
				t.Errorf("IsProdRuntime() with %s=%q = %v, want %v",
					common.QSpillControllerRuntimeEnv, tc.val, got, tc.want)
			}
		})
	}
}
