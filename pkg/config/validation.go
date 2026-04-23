package config

import (
	"errors"
	"fmt"

	"github.com/pzhenzhou/qspill-controller/pkg/api"
)

// validateRegistry runs every semantic rule against the assembled registry
// and joins the results into a single error so an operator can see all
// problems in one round trip rather than discovering them one restart at a
// time. Returns nil when the registry is fully valid.
//
// Out-of-range Hysteresis is *not* a validation error; it is silently
// clamped to [0,1] during conversion (see config.go) per the design's
// "define errors out of existence" stance for that knob.
func validateRegistry(r *PolicyRegistry) error {
	logger.Info("validating policy registry",
		"policies", len(r.policies),
		"action", string(r.defaults.Action),
	)
	var errs []error

	if err := validateDefaults(&r.defaults); err != nil {
		errs = append(errs, fmt.Errorf("defaults: %w", err))
	}

	seenName := make(map[string]int, len(r.policies))
	seenQueue := make(map[string]int, len(r.policies))
	for i, p := range r.policies {
		if err := validatePolicy(p); err != nil {
			errs = append(errs, fmt.Errorf("policies[%d] (%q): %w", i, p.Name, err))
		}
		if p.Name != "" {
			if prior, dup := seenName[p.Name]; dup {
				errs = append(errs, fmt.Errorf("policies[%d] (%q): duplicate policy name; previously defined at policies[%d]", i, p.Name, prior))
			} else {
				seenName[p.Name] = i
			}
		}
		if p.QueueName != "" {
			if prior, dup := seenQueue[p.QueueName]; dup {
				errs = append(errs, fmt.Errorf("policies[%d] (%q): queueName %q already claimed by policies[%d]", i, p.Name, p.QueueName, prior))
			} else {
				seenQueue[p.QueueName] = i
			}
		}
	}

	if joined := errors.Join(errs...); joined != nil {
		logger.Error(joined, "policy registry validation failed",
			"errCount", len(errs),
			"policies", len(r.policies),
		)
		return joined
	}
	return nil
}

// validateDefaults checks the controller-wide defaults block. Every field is
// required and must yield a workable value because per-policy thresholds are
// layered on top of these defaults.
func validateDefaults(d *Defaults) error {
	var errs []error
	if d.NodeGroupLabelKey == "" {
		errs = append(errs, errors.New("nodeGroupLabelKey is required"))
	}
	if d.Action != api.ActionModeNope && d.Action != api.ActionModePatch {
		errs = append(errs, fmt.Errorf("action must be one of [%q, %q], got %q", api.ActionModeNope, api.ActionModePatch, d.Action))
	}
	if d.ReconcileResyncPeriod <= 0 {
		errs = append(errs, errors.New("reconcileResyncPeriod must be > 0"))
	}
	if err := validateThresholds(&d.Thresholds); err != nil {
		errs = append(errs, fmt.Errorf("thresholds: %w", err))
	}
	return errors.Join(errs...)
}

// validatePolicy checks the per-policy invariants that must hold *after*
// defaults have been merged in. The merge happens in config.go before we get
// here, so any zero threshold here is a real error rather than an inheritance
// gap.
func validatePolicy(p *api.SpillPolicy) error {
	var errs []error
	if p.Name == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if p.QueueName == "" {
		errs = append(errs, errors.New("queueName is required"))
	}
	if p.DedicatedNodeGroup == "" {
		errs = append(errs, errors.New("dedicatedNodeGroup is required"))
	}
	if p.OverflowNodeGroup == "" {
		errs = append(errs, errors.New("overflowNodeGroup is required"))
	}
	if p.DedicatedNodeGroup != "" && p.DedicatedNodeGroup == p.OverflowNodeGroup {
		errs = append(errs, fmt.Errorf("dedicatedNodeGroup and overflowNodeGroup must differ (both = %q)", p.DedicatedNodeGroup))
	}
	if err := validateThresholds(&p.Thresholds); err != nil {
		errs = append(errs, fmt.Errorf("thresholds: %w", err))
	}
	return errors.Join(errs...)
}

// validateThresholds asserts the time knobs are positive. Hysteresis is
// excluded by design: invalid values are clamped to [0,1] earlier and never
// reach this function as an error.
func validateThresholds(t *api.Thresholds) error {
	var errs []error
	if t.TimeOn <= 0 {
		errs = append(errs, errors.New("timeOn must be > 0"))
	}
	if t.TimeOff <= 0 {
		errs = append(errs, errors.New("timeOff must be > 0"))
	}
	if t.TimePendingMax <= 0 {
		errs = append(errs, errors.New("timePendingMax must be > 0"))
	}
	return errors.Join(errs...)
}
