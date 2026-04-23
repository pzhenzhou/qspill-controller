package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

// configEnvVars enumerates every env var the loader honours. Tests mutate the
// process environment via t.Setenv and rely on this list to scrub inherited
// values before each test, so a developer with QSPILL_CONTROLLER_* set in
// their shell does not silently change test outcomes.
var configEnvVars = []string{
	config.EnvConfigFile,
	config.EnvNodeGroupLabelKey,
	config.EnvAction,
	config.EnvReconcileResyncPeriod,
	config.EnvThresholdsTimeOn,
	config.EnvThresholdsTimeOff,
	config.EnvThresholdsTimePending,
	config.EnvThresholdsHysteresis,
}

// clearConfigEnv unsets every loader env var for the duration of the test.
// Setting to the empty string takes advantage of envKeyTransform's empty-skip:
// koanf sees no value and inherits whatever the file or defaults provided.
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range configEnvVars {
		t.Setenv(name, "")
	}
}

// readFixture loads a YAML/JSON file from testdata; tests fail fast on read
// errors so the table cases stay focused on what they assert.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return data
}

// writeTempFile drops the named file into the test's tempdir and returns its
// absolute path. Used by the Load() tests to drive file-path discovery
// through EnvConfigFile.
func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write temp file %q: %v", path, err)
	}
	return path
}

func TestLoadFromBytesValidBaseline(t *testing.T) {
	clearConfigEnv(t)

	reg, err := config.LoadFromBytes(readFixture(t, "valid_baseline.yaml"))
	if err != nil {
		t.Fatalf("LoadFromBytes returned error: %v", err)
	}
	if reg == nil {
		t.Fatal("LoadFromBytes returned nil registry")
	}

	d := reg.Defaults()
	if d.NodeGroupLabelKey != "volcano.sh/nodegroup-name" {
		t.Errorf("NodeGroupLabelKey: got %q", d.NodeGroupLabelKey)
	}
	if d.Action != api.ActionModeNope {
		t.Errorf("Action: got %q, want %q", d.Action, api.ActionModeNope)
	}
	if d.ReconcileResyncPeriod != 30*time.Second {
		t.Errorf("ReconcileResyncPeriod: got %v, want 30s", d.ReconcileResyncPeriod)
	}
	if d.Thresholds.TimeOn != 30*time.Second ||
		d.Thresholds.TimeOff != 10*time.Minute ||
		d.Thresholds.TimePendingMax != 5*time.Minute ||
		d.Thresholds.Hysteresis != 0.2 {
		t.Errorf("defaults thresholds drift: %+v", d.Thresholds)
	}

	policies := reg.Policies()
	if got, want := len(policies), 2; got != want {
		t.Fatalf("policies: got %d, want %d", got, want)
	}

	a := policies[0]
	if a.Name != "biz-a" || a.QueueName != "biz-a" ||
		a.DedicatedNodeGroup != "ng2" || a.OverflowNodeGroup != "ng1" ||
		a.MinNodes != 1 || a.MaxNodes != 5 {
		t.Errorf("biz-a fields drift: %+v", a)
	}
	if a.Thresholds != d.Thresholds {
		t.Errorf("biz-a should inherit defaults thresholds; got %+v want %+v", a.Thresholds, d.Thresholds)
	}
	gotByQueue, ok := reg.PolicyByQueue("biz-a")
	if !ok || gotByQueue != a {
		t.Errorf("PolicyByQueue(biz-a): got (%v,%v), want (%v,true)", gotByQueue, ok, a)
	}

	b := policies[1]
	if b.Thresholds.TimeOn != 1*time.Minute {
		t.Errorf("biz-b TimeOn override: got %v want 1m", b.Thresholds.TimeOn)
	}
	if b.Thresholds.TimeOff != d.Thresholds.TimeOff {
		t.Errorf("biz-b TimeOff should inherit; got %v want %v", b.Thresholds.TimeOff, d.Thresholds.TimeOff)
	}
	if b.Thresholds.Hysteresis != 0.5 {
		t.Errorf("biz-b Hysteresis override: got %v want 0.5", b.Thresholds.Hysteresis)
	}
}

// TestLoadFromBytesValidMinimal exercises the defaults-inheritance path:
// fields the operator omits in YAML inherit from the loader's built-in
// defaults rather than collapsing to zero values. The fixture omits
// hysteresis from the defaults block; the policy must inherit the built-in
// 0.2 (which is in turn inherited by the policy that has no per-policy
// thresholds override).
func TestLoadFromBytesValidMinimal(t *testing.T) {
	clearConfigEnv(t)

	reg, err := config.LoadFromBytes(readFixture(t, "valid_minimal.yaml"))
	if err != nil {
		t.Fatalf("LoadFromBytes returned error: %v", err)
	}
	policies := reg.Policies()
	if len(policies) != 1 {
		t.Fatalf("policies: got %d", len(policies))
	}
	if got, want := policies[0].Thresholds.Hysteresis, 0.2; got != want {
		t.Errorf("absent hysteresis should inherit defaults (%v); got %v", want, got)
	}
	if reg.Defaults().Action != api.ActionModePatch {
		t.Errorf("Action: got %q", reg.Defaults().Action)
	}
}

// TestLoadFromBytesHysteresisClampedSilently asserts that out-of-range
// hysteresis is clamped to [0,1] without producing a validation error, per
// the design's "validation clamps" rule for this knob.
func TestLoadFromBytesHysteresisClampedSilently(t *testing.T) {
	clearConfigEnv(t)

	reg, err := config.LoadFromBytes(readFixture(t, "valid_hysteresis_clamp_high.yaml"))
	if err != nil {
		t.Fatalf("clamping should not produce a validation error; got %v", err)
	}
	if got := reg.Defaults().Thresholds.Hysteresis; got != 1 {
		t.Errorf("defaults hysteresis 1.7 should clamp to 1.0, got %v", got)
	}
	policies := reg.Policies()
	if len(policies) != 1 {
		t.Fatalf("policies: got %d", len(policies))
	}
	if got := policies[0].Thresholds.Hysteresis; got != 0 {
		t.Errorf("policy hysteresis -0.5 should clamp to 0.0, got %v", got)
	}
}

// TestLoadFromBytesInvalid covers every validation branch via dedicated
// fixtures so an accidental loosening of validation surfaces as a failing
// test rather than a silent regression. Each case asserts the substring most
// uniquely tied to its branch.
func TestLoadFromBytesInvalid(t *testing.T) {
	cases := []struct {
		name     string
		fixture  string
		wantSubs []string
	}{
		{"missing_name", "invalid_missing_name.yaml", []string{"name is required"}},
		{"missing_queue", "invalid_missing_queue.yaml", []string{"queueName is required"}},
		{"missing_dedicated", "invalid_missing_dedicated.yaml", []string{"dedicatedNodeGroup is required"}},
		{"missing_overflow", "invalid_missing_overflow.yaml", []string{"overflowNodeGroup is required"}},
		{"dedicated_eq_overflow", "invalid_dedicated_eq_overflow.yaml", []string{"must differ"}},
		{"duplicate_name", "invalid_duplicate_name.yaml", []string{"duplicate policy name"}},
		{"duplicate_queue", "invalid_duplicate_queue.yaml", []string{"already claimed"}},
		{"zero_timeon", "invalid_zero_timeon.yaml", []string{"timeOn must be > 0"}},
		{"zero_timeoff", "invalid_zero_timeoff.yaml", []string{"timeOff must be > 0"}},
		{"zero_timependingmax", "invalid_zero_timependingmax.yaml", []string{"timePendingMax must be > 0"}},
		{"missing_nodegroup_label", "invalid_missing_nodegroup_label.yaml", []string{"nodeGroupLabelKey is required"}},
		{"invalid_action", "invalid_action.yaml", []string{"action must be one of"}},
		{"zero_resync", "invalid_zero_resync.yaml", []string{"reconcileResyncPeriod must be > 0"}},
		{"malformed_yaml", "invalid_yaml.yaml", []string{"parse yaml"}},
		{"bad_duration", "invalid_bad_duration.yaml", []string{"invalid duration"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			clearConfigEnv(t)
			reg, err := config.LoadFromBytes(readFixture(t, tc.fixture))
			if err == nil {
				t.Fatalf("LoadFromBytes(%s) returned no error", tc.fixture)
			}
			if reg != nil {
				t.Errorf("LoadFromBytes(%s) returned non-nil registry on error", tc.fixture)
			}
			msg := err.Error()
			for _, want := range tc.wantSubs {
				if !strings.Contains(msg, want) {
					t.Errorf("error message missing substring %q\nfull: %s", want, msg)
				}
			}
		})
	}
}

// TestLoadFromBytesAccumulatesValidationErrors asserts the loader surfaces
// every problem in one error so an operator iterating on a config does not
// have to fix-restart-fix-restart for each issue independently.
func TestLoadFromBytesAccumulatesValidationErrors(t *testing.T) {
	clearConfigEnv(t)

	const yaml = `
defaults:
  nodeGroupLabelKey: ""
  action: Bogus
  reconcileResyncPeriod: 30s
  thresholds:
    timeOn: 30s
    timeOff: 10m
    timePendingMax: 5m
policies:
  - name: ""
    queueName: ""
    dedicatedNodeGroup: ""
    overflowNodeGroup: ""
`
	_, err := config.LoadFromBytes([]byte(yaml))
	if err == nil {
		t.Fatal("LoadFromBytes returned no error")
	}
	msg := err.Error()
	required := []string{
		"nodeGroupLabelKey is required",
		"action must be one of",
		"name is required",
		"queueName is required",
		"dedicatedNodeGroup is required",
		"overflowNodeGroup is required",
	}
	for _, want := range required {
		if !strings.Contains(msg, want) {
			t.Errorf("expected accumulated error to contain %q\nfull: %s", want, msg)
		}
	}
}

// TestLoadFromBytesEmptyUsesDefaults: with no bytes and no env, the loader
// must still return a valid registry populated entirely from the built-in
// defaults. The policies list is empty (operators have not declared any),
// which is a deliberately permitted state — the controller idles cleanly
// until a real config is installed.
func TestLoadFromBytesEmptyUsesDefaults(t *testing.T) {
	clearConfigEnv(t)

	reg, err := config.LoadFromBytes(nil)
	if err != nil {
		t.Fatalf("LoadFromBytes(nil) returned error: %v", err)
	}
	if reg == nil {
		t.Fatal("LoadFromBytes(nil) returned nil registry")
	}
	d := reg.Defaults()
	if d.NodeGroupLabelKey == "" || d.Action == "" {
		t.Errorf("defaults should be populated from defaultConfig: %+v", d)
	}
	if d.Thresholds.TimeOn <= 0 || d.Thresholds.TimeOff <= 0 || d.Thresholds.TimePendingMax <= 0 {
		t.Errorf("default thresholds should be positive: %+v", d.Thresholds)
	}
	if got := len(reg.Policies()); got != 0 {
		t.Errorf("expected 0 policies, got %d", got)
	}
}

// TestLoadFromBytesEnvOverridesDefaults sets every env var and supplies no
// bytes. Each env value must surface in the resulting registry, proving the
// allowlist mapping is wired correctly and the duration decode hook handles
// every threshold field.
func TestLoadFromBytesEnvOverridesDefaults(t *testing.T) {
	clearConfigEnv(t)

	t.Setenv(config.EnvNodeGroupLabelKey, "topology.example.com/group")
	t.Setenv(config.EnvAction, "Patch")
	t.Setenv(config.EnvReconcileResyncPeriod, "90s")
	t.Setenv(config.EnvThresholdsTimeOn, "45s")
	t.Setenv(config.EnvThresholdsTimeOff, "15m")
	t.Setenv(config.EnvThresholdsTimePending, "7m")
	t.Setenv(config.EnvThresholdsHysteresis, "0.4")

	reg, err := config.LoadFromBytes(nil)
	if err != nil {
		t.Fatalf("LoadFromBytes returned error: %v", err)
	}
	d := reg.Defaults()

	if d.NodeGroupLabelKey != "topology.example.com/group" {
		t.Errorf("NodeGroupLabelKey: got %q", d.NodeGroupLabelKey)
	}
	if d.Action != api.ActionModePatch {
		t.Errorf("Action: got %q", d.Action)
	}
	if d.ReconcileResyncPeriod != 90*time.Second {
		t.Errorf("ReconcileResyncPeriod: got %v", d.ReconcileResyncPeriod)
	}
	if d.Thresholds.TimeOn != 45*time.Second {
		t.Errorf("TimeOn: got %v", d.Thresholds.TimeOn)
	}
	if d.Thresholds.TimeOff != 15*time.Minute {
		t.Errorf("TimeOff: got %v", d.Thresholds.TimeOff)
	}
	if d.Thresholds.TimePendingMax != 7*time.Minute {
		t.Errorf("TimePendingMax: got %v", d.Thresholds.TimePendingMax)
	}
	if d.Thresholds.Hysteresis != 0.4 {
		t.Errorf("Hysteresis: got %v", d.Thresholds.Hysteresis)
	}
}

// TestLoadFromBytesEnvBeatsFile pins down the precedence order: when both
// the bytes (file source) and env supply a value for the same key, env wins.
func TestLoadFromBytesEnvBeatsFile(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv(config.EnvAction, "Patch")
	t.Setenv(config.EnvThresholdsTimeOn, "12s")

	reg, err := config.LoadFromBytes(readFixture(t, "valid_baseline.yaml"))
	if err != nil {
		t.Fatalf("LoadFromBytes returned error: %v", err)
	}
	d := reg.Defaults()
	if d.Action != api.ActionModePatch {
		t.Errorf("Action should be Patch (env override); got %q", d.Action)
	}
	if d.Thresholds.TimeOn != 12*time.Second {
		t.Errorf("TimeOn should be 12s (env override); got %v", d.Thresholds.TimeOn)
	}
	// Fields without env override must keep the file value.
	if d.Thresholds.TimeOff != 10*time.Minute {
		t.Errorf("TimeOff should keep file value 10m; got %v", d.Thresholds.TimeOff)
	}
}

// TestLoadFromBytesEnvEmptyValueIgnored asserts that explicitly-empty env
// values do not clobber file/default values. A stray `KEY=` in an operator's
// shell would otherwise force a required field empty and break validation.
func TestLoadFromBytesEnvEmptyValueIgnored(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv(config.EnvAction, "")

	reg, err := config.LoadFromBytes(readFixture(t, "valid_baseline.yaml"))
	if err != nil {
		t.Fatalf("LoadFromBytes returned error: %v", err)
	}
	if got := reg.Defaults().Action; got != api.ActionModeNope {
		t.Errorf("empty env should not override file value; got Action=%q want %q", got, api.ActionModeNope)
	}
}

// TestLoadFromBytesEnvDoesNotTouchPolicies guards the design choice that the
// policies list is file-only. Setting an env var that maps to nothing inside
// envKeyMap (here, a hypothetical policy override) must be ignored entirely.
func TestLoadFromBytesEnvDoesNotTouchPolicies(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("QSPILL_CONTROLLER_POLICIES_0_NAME", "hijacked")

	reg, err := config.LoadFromBytes(readFixture(t, "valid_baseline.yaml"))
	if err != nil {
		t.Fatalf("LoadFromBytes returned error: %v", err)
	}
	policies := reg.Policies()
	if len(policies) == 0 || policies[0].Name != "biz-a" {
		t.Errorf("env outside allowlist must not mutate policies; got policies=%v", policies)
	}
}

func TestLoadFromFileYAML(t *testing.T) {
	clearConfigEnv(t)
	path := writeTempFile(t, "config.yaml", readFixture(t, "valid_baseline.yaml"))
	t.Setenv(config.EnvConfigFile, path)

	reg, err := config.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := len(reg.Policies()); got != 2 {
		t.Errorf("policies: got %d want 2", got)
	}
}

// TestLoadFromFileJSON exercises the multi-format selectParser path through
// the .json extension. The returned registry must be structurally identical
// to a YAML-derived one, proving the wire format is interchangeable.
func TestLoadFromFileJSON(t *testing.T) {
	clearConfigEnv(t)
	path := writeTempFile(t, "config.json", readFixture(t, "valid_baseline.json"))
	t.Setenv(config.EnvConfigFile, path)

	reg, err := config.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	d := reg.Defaults()
	if d.Action != api.ActionModePatch {
		t.Errorf("Action: got %q want Patch", d.Action)
	}
	if d.ReconcileResyncPeriod != 45*time.Second {
		t.Errorf("ReconcileResyncPeriod: got %v want 45s", d.ReconcileResyncPeriod)
	}
	policies := reg.Policies()
	if len(policies) != 1 || policies[0].Name != "biz-json" {
		t.Errorf("policies: got %+v", policies)
	}
}

// TestLoadFromFileEnvOverridesFile pins down the same precedence at the
// file-driven entry point: env beats a value the file otherwise sets.
func TestLoadFromFileEnvOverridesFile(t *testing.T) {
	clearConfigEnv(t)
	path := writeTempFile(t, "config.yaml", readFixture(t, "valid_baseline.yaml"))
	t.Setenv(config.EnvConfigFile, path)
	t.Setenv(config.EnvAction, "Patch")

	reg, err := config.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := reg.Defaults().Action; got != api.ActionModePatch {
		t.Errorf("Action should be Patch via env; got %q", got)
	}
}

func TestLoadFromFileMissingPath(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv(config.EnvConfigFile, filepath.Join(t.TempDir(), "does-not-exist.yaml"))

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load with nonexistent file should error")
	}
	if !strings.Contains(err.Error(), "load file") {
		t.Errorf("error should mention file loading; got %v", err)
	}
}

func TestLoadFromFileUnsupportedExtension(t *testing.T) {
	clearConfigEnv(t)
	path := writeTempFile(t, "config.txt", []byte("ignored"))
	t.Setenv(config.EnvConfigFile, path)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load with .txt should error")
	}
	if !strings.Contains(err.Error(), "unsupported file extension") {
		t.Errorf("error should mention unsupported extension; got %v", err)
	}
}

// TestLoadUnsetFileUsesDefaults asserts that running with no EnvConfigFile and
// no env overrides yields a registry populated from the built-in defaults
// only. This is the same shape as LoadFromBytes(nil) — the file source is
// truly optional at the cold-start entry point.
func TestLoadUnsetFileUsesDefaults(t *testing.T) {
	clearConfigEnv(t)

	reg, err := config.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := reg.Defaults().Action; got != api.ActionModeNope {
		t.Errorf("default Action should be Nope; got %q", got)
	}
	if got := len(reg.Policies()); got != 0 {
		t.Errorf("expected 0 policies with no file, got %d", got)
	}
}
