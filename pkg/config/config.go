package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/common"
)

// logger is the per-module logr.Logger for the config loader. Module-scoped
// instance lets operators filter lines like `module=config` to follow a
// configuration reload from file/env merge through validation.
var logger = common.NewLogger("config")

// Project-namespaced environment variable names. Operators set EnvConfigFile
// to point Load at a file on disk; the remaining variables override individual
// fields of the defaults block. Names use UPPER_SNAKE_CASE so they survive
// shell quoting and Kubernetes downward API conventions.
const (
	EnvConfigFile = "QSPILL_CONTROLLER_CONFIG"

	EnvNodeGroupLabelKey     = "QSPILL_CONTROLLER_NODE_GROUP_LABEL_KEY"
	EnvAction                = "QSPILL_CONTROLLER_ACTION"
	EnvReconcileResyncPeriod = "QSPILL_CONTROLLER_RECONCILE_RESYNC_PERIOD"
	EnvThresholdsTimeOn      = "QSPILL_CONTROLLER_THRESHOLDS_TIME_ON"
	EnvThresholdsTimeOff     = "QSPILL_CONTROLLER_THRESHOLDS_TIME_OFF"
	EnvThresholdsTimePending = "QSPILL_CONTROLLER_THRESHOLDS_TIME_PENDING_MAX"
	EnvThresholdsHysteresis  = "QSPILL_CONTROLLER_THRESHOLDS_HYSTERESIS"
)

// envKeyMap pins every supported env var to its koanf path. The env provider
// drops any environment variable not present here, so unrelated host variables
// (PATH, HOME, etc.) cannot accidentally bleed into the configuration. Only
// the defaults block is exposed: per-policy fields are managed exclusively via
// the file source because env-driven array indexing makes for unreviewable ops.
var envKeyMap = map[string]string{
	EnvNodeGroupLabelKey:     "defaults.nodeGroupLabelKey",
	EnvAction:                "defaults.action",
	EnvReconcileResyncPeriod: "defaults.reconcileResyncPeriod",
	EnvThresholdsTimeOn:      "defaults.thresholds.timeOn",
	EnvThresholdsTimeOff:     "defaults.thresholds.timeOff",
	EnvThresholdsTimePending: "defaults.thresholds.timePendingMax",
	EnvThresholdsHysteresis:  "defaults.thresholds.hysteresis",
}

// Load assembles a *PolicyRegistry from three layered sources, in increasing
// precedence: built-in defaults, an optional file pointed at by EnvConfigFile,
// and environment variable overrides drawn from envKeyMap. The file is parsed
// based on its extension (.yaml/.yml, .json, .toml) so the same loader serves
// every supported wire format.
//
// Returns a fully-built, immutable registry on success and an aggregated
// validation error on failure. Callers are expected to be fail-closed: keep
// the prior registry on any non-nil error.
func Load() (*PolicyRegistry, error) {
	configPath := strings.TrimSpace(os.Getenv(EnvConfigFile))
	logger.Info("loading configuration from layered sources",
		"source", "defaults+file+env",
		"configFileEnv", EnvConfigFile,
		"configFile", configPath,
	)
	k, err := newKoanfWithDefaults()
	if err != nil {
		logger.Error(err, "load defaults failed")
		return nil, err
	}
	if err := loadFileIfConfigured(k); err != nil {
		logger.Error(err, "load file failed", "configFile", configPath)
		return nil, err
	}
	if err := loadEnv(k); err != nil {
		logger.Error(err, "load env failed")
		return nil, err
	}
	reg, err := finalize(k)
	if err != nil {
		logger.Error(err, "finalize registry failed")
		return nil, err
	}
	logger.Info("configuration loaded",
		"policies", len(reg.policies),
		"action", string(reg.defaults.Action),
		"reconcileResyncPeriod", reg.defaults.ReconcileResyncPeriod.String(),
	)
	return reg, nil
}

// LoadFromBytes is the bytes-driven counterpart of Load. It is the entry point
// used by the ConfigMap reload path: the watcher reads the ConfigMap data,
// passes the bytes here, and installs the resulting registry on success.
//
// Same precedence as Load (defaults < bytes < env) so operators get a
// consistent override story across cold start and hot reload. The bytes are
// always parsed as YAML — that is the controller ConfigMap convention.
func LoadFromBytes(data []byte) (*PolicyRegistry, error) {
	logger.Info("loading configuration from bytes",
		"source", "defaults+bytes+env",
		"bytes", len(data),
	)
	k, err := newKoanfWithDefaults()
	if err != nil {
		logger.Error(err, "load defaults failed")
		return nil, err
	}
	if len(data) > 0 {
		if err := k.Load(rawbytes.Provider(data), yaml.Parser()); err != nil {
			wrapped := fmt.Errorf("config: parse yaml: %w", err)
			logger.Error(wrapped, "parse yaml bytes failed", "bytes", len(data))
			return nil, wrapped
		}
	}
	if err := loadEnv(k); err != nil {
		logger.Error(err, "load env failed")
		return nil, err
	}
	reg, err := finalize(k)
	if err != nil {
		logger.Error(err, "finalize registry failed")
		return nil, err
	}
	logger.Info("configuration loaded from bytes",
		"policies", len(reg.policies),
		"action", string(reg.defaults.Action),
	)
	return reg, nil
}

// newKoanfWithDefaults seeds a fresh Koanf instance with built-in defaults
// derived from defaultConfig(). Subsequent provider loads overlay these values.
func newKoanfWithDefaults() (*koanf.Koanf, error) {
	k := koanf.New(".")
	defaults := defaultConfig()
	if err := k.Load(structs.Provider(defaults, "koanf"), nil); err != nil {
		wrapped := fmt.Errorf("config: load defaults: %w", err)
		logger.Error(wrapped, "load defaults into koanf failed")
		return nil, wrapped
	}
	return k, nil
}

// loadFileIfConfigured loads the file pointed at by EnvConfigFile, if set and
// non-blank. An unset variable is not an error: operators may run the
// controller with defaults plus env overrides only.
func loadFileIfConfigured(k *koanf.Koanf) error {
	path := strings.TrimSpace(os.Getenv(EnvConfigFile))
	if path == "" {
		logger.Info("no config file configured, skipping file source",
			"env", EnvConfigFile,
		)
		return nil
	}
	logger.Info("loading config file", "path", path)
	parser, err := selectParser(path)
	if err != nil {
		logger.Error(err, "select parser failed", "path", path)
		return err
	}
	if err := k.Load(file.Provider(path), parser); err != nil {
		wrapped := fmt.Errorf("config: load file %q: %w", path, err)
		logger.Error(wrapped, "load config file failed", "path", path)
		return wrapped
	}
	return nil
}

// loadEnv applies the env provider with the explicit allowlist. The "."
// delimiter tells the provider to unflatten dotted koanf paths returned by
// the transform (e.g. "defaults.action") into the nested map shape that
// koanf merges into struct fields. The transform itself returns either a
// fully-qualified path or an empty string, and koanf drops empty keys.
func loadEnv(k *koanf.Koanf) error {
	if err := k.Load(env.Provider(".", env.Opt{TransformFunc: envKeyTransform}), nil); err != nil {
		wrapped := fmt.Errorf("config: load env: %w", err)
		logger.Error(wrapped, "load env provider failed")
		return wrapped
	}
	return nil
}

// envKeyTransform maps an environment variable name to its koanf path via the
// allowlist, dropping unknown names and empty values. Treating empty values as
// unset prevents a stray `KEY=` in the operator's shell from clobbering an
// otherwise valid file value with a forced empty string.
func envKeyTransform(key, value string) (string, any) {
	mapped, ok := envKeyMap[strings.ToUpper(key)]
	if !ok {
		return "", nil
	}
	if value == "" {
		return "", nil
	}
	return mapped, value
}

// finalize decodes the merged koanf state into the wire types, builds the
// typed registry, runs validation, and returns the immutable result.
func finalize(k *koanf.Koanf) (*PolicyRegistry, error) {
	var raw rawConfig
	unmarshalConf := koanf.UnmarshalConf{
		Tag: "koanf",
		DecoderConfig: &mapstructure.DecoderConfig{
			Result:           &raw,
			TagName:          "koanf",
			WeaklyTypedInput: true,
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
			),
		},
	}
	if err := k.UnmarshalWithConf("", &raw, unmarshalConf); err != nil {
		wrapped := fmt.Errorf("config: decode: %w", err)
		logger.Error(wrapped, "decode merged koanf state failed")
		return nil, wrapped
	}

	defaults := buildDefaults(&raw.Defaults)
	policies := buildPolicies(raw.Policies, defaults)
	reg := newPolicyRegistry(defaults, policies)

	if err := validateRegistry(reg); err != nil {
		wrapped := fmt.Errorf("config: invalid: %w", err)
		logger.Error(wrapped, "registry validation failed",
			"policies", len(policies),
		)
		return nil, wrapped
	}
	return reg, nil
}

// selectParser maps a file extension to the matching koanf parser. New formats
// gate here; everything else in the loader stays format-agnostic.
func selectParser(path string) (koanf.Parser, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		return yaml.Parser(), nil
	case ".json":
		return json.Parser(), nil
	case ".toml":
		return toml.Parser(), nil
	default:
		err := fmt.Errorf("config: unsupported file extension %q (want .yaml, .yml, .json, .toml)", ext)
		logger.Error(err, "unsupported config file extension", "path", path, "ext", ext)
		return nil, err
	}
}

// rawConfig mirrors the wire schema. Field types are pinned to what koanf
// emits after merging all providers; downstream build helpers convert these
// into the api types so validation and runtime code never deal with raw maps.
type rawConfig struct {
	Defaults rawDefaults `koanf:"defaults"`
	Policies []rawPolicy `koanf:"policies"`
}

// rawDefaults uses value types because the defaults block is always fully
// populated after defaultConfig() seeds it. File or env may override fields,
// but the fields themselves are never absent at decode time.
type rawDefaults struct {
	NodeGroupLabelKey     string               `koanf:"nodeGroupLabelKey"`
	Action                string               `koanf:"action"`
	ReconcileResyncPeriod time.Duration        `koanf:"reconcileResyncPeriod"`
	Thresholds            rawDefaultThresholds `koanf:"thresholds"`
}

// rawDefaultThresholds is the value-typed thresholds carried by the defaults
// block. Hysteresis is normalised in buildDefaults so the rest of the system
// never sees an out-of-range value.
type rawDefaultThresholds struct {
	TimeOn         time.Duration `koanf:"timeOn"`
	TimeOff        time.Duration `koanf:"timeOff"`
	TimePendingMax time.Duration `koanf:"timePendingMax"`
	Hysteresis     float64       `koanf:"hysteresis"`
}

// rawPolicy is one entry of the policies list. Per-policy thresholds use a
// pointer so the loader can distinguish "field absent in YAML" (inherit
// defaults) from "field explicitly set" (override). Operators may override a
// subset of threshold fields; unspecified fields inherit from defaults.
type rawPolicy struct {
	Name               string               `koanf:"name"`
	QueueName          string               `koanf:"queueName"`
	DedicatedNodeGroup string               `koanf:"dedicatedNodeGroup"`
	OverflowNodeGroup  string               `koanf:"overflowNodeGroup"`
	MinNodes           int                  `koanf:"minNodes"`
	MaxNodes           int                  `koanf:"maxNodes"`
	Thresholds         *rawPolicyThresholds `koanf:"thresholds,omitempty"`
}

// rawPolicyThresholds carries per-field overrides. Pointers convey
// "specified-or-not" so partial overrides work: `thresholds: {timeOn: 1m}`
// overrides only TimeOn and inherits the other three from defaults.
type rawPolicyThresholds struct {
	TimeOn         *time.Duration `koanf:"timeOn,omitempty"`
	TimeOff        *time.Duration `koanf:"timeOff,omitempty"`
	TimePendingMax *time.Duration `koanf:"timePendingMax,omitempty"`
	Hysteresis     *float64       `koanf:"hysteresis,omitempty"`
}

// defaultConfig returns the built-in defaults that seed the loader. Operators
// who omit any field in their config get these values; specifying a field in
// the file or via env overrides the corresponding default. Threshold values
// align with the design's recommended baseline: short TimeOn for snappy spill,
// long TimeOff to dampen flapping, modest hysteresis.
func defaultConfig() rawConfig {
	return rawConfig{
		Defaults: rawDefaults{
			NodeGroupLabelKey:     "volcano.sh/nodegroup-name",
			Action:                string(api.ActionModeNope),
			ReconcileResyncPeriod: 30 * time.Second,
			Thresholds: rawDefaultThresholds{
				TimeOn:         30 * time.Second,
				TimeOff:        10 * time.Minute,
				TimePendingMax: 5 * time.Minute,
				Hysteresis:     0.2,
			},
		},
	}
}

// buildDefaults converts the raw defaults block into a typed Defaults value.
// Hysteresis is clamped to [0,1] right here so downstream code never has to
// defend against out-of-range values, regardless of source (file or env).
func buildDefaults(raw *rawDefaults) Defaults {
	return Defaults{
		NodeGroupLabelKey:     raw.NodeGroupLabelKey,
		Action:                api.ActionMode(raw.Action),
		ReconcileResyncPeriod: raw.ReconcileResyncPeriod,
		Thresholds: api.Thresholds{
			TimeOn:         raw.Thresholds.TimeOn,
			TimeOff:        raw.Thresholds.TimeOff,
			TimePendingMax: raw.Thresholds.TimePendingMax,
			Hysteresis:     clampUnit(raw.Thresholds.Hysteresis),
		},
	}
}

// buildPolicies converts every raw policy entry into a fully-typed
// *api.SpillPolicy with merged thresholds. Validation runs after this step
// against the api types, so all conversions stay in one place.
func buildPolicies(rawPolicies []rawPolicy, defaults Defaults) []*api.SpillPolicy {
	out := make([]*api.SpillPolicy, 0, len(rawPolicies))
	for i := range rawPolicies {
		out = append(out, buildPolicy(&rawPolicies[i], defaults))
	}
	return out
}

func buildPolicy(raw *rawPolicy, defaults Defaults) *api.SpillPolicy {
	return &api.SpillPolicy{
		Name:               raw.Name,
		QueueName:          raw.QueueName,
		DedicatedNodeGroup: raw.DedicatedNodeGroup,
		OverflowNodeGroup:  raw.OverflowNodeGroup,
		MinNodes:           raw.MinNodes,
		MaxNodes:           raw.MaxNodes,
		Thresholds:         thresholdsFromRaw(raw.Thresholds, defaults.Thresholds),
	}
}

// thresholdsFromRaw overlays an optional per-policy thresholds block onto a
// base. Each field is overwritten only when the operator explicitly set it
// (raw value is non-nil), matching the design's policy-level override rule.
// Hysteresis is clamped to [0,1] so the rest of the system never has to
// defend against out-of-range values.
func thresholdsFromRaw(raw *rawPolicyThresholds, base api.Thresholds) api.Thresholds {
	out := base
	if raw == nil {
		return out
	}
	if raw.TimeOn != nil {
		out.TimeOn = *raw.TimeOn
	}
	if raw.TimeOff != nil {
		out.TimeOff = *raw.TimeOff
	}
	if raw.TimePendingMax != nil {
		out.TimePendingMax = *raw.TimePendingMax
	}
	if raw.Hysteresis != nil {
		out.Hysteresis = clampUnit(*raw.Hysteresis)
	}
	return out
}

// clampUnit pins x to the closed interval [0, 1].
func clampUnit(x float64) float64 {
	switch {
	case x < 0:
		return 0
	case x > 1:
		return 1
	default:
		return x
	}
}
