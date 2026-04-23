// Package leader provides a thin wrapper over client-go's leader election,
// encapsulating the LeaseLock construction and callback wiring so the caller
// only needs to supply a Config and call Run.
package leader

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/pzhenzhou/qspill-controller/pkg/common"
)

// logger is the per-module logr.Logger for leader election. Lines are tagged
// `module=leader` so operators can isolate the lease lifecycle (validate ->
// elect -> won/lost) from controller traffic.
var logger = common.NewLogger("leader")

// Config carries everything Run needs to participate in leader election.
type Config struct {
	KubeClient       kubernetes.Interface
	Namespace        string
	LeaseName        string
	Identity         string
	LeaseDuration    time.Duration
	RenewDeadline    time.Duration
	RetryPeriod      time.Duration
	OnStartedLeading func(context.Context)
	OnStoppedLeading func()
}

// Validate returns an error if any required field is missing or any timing
// constraint is violated.
func (c *Config) Validate() error {
	if c.KubeClient == nil {
		err := errors.New("leader: KubeClient is required")
		logger.Error(err, "leader config invalid")
		return err
	}
	if c.Namespace == "" {
		err := errors.New("leader: Namespace is required")
		logger.Error(err, "leader config invalid")
		return err
	}
	if c.LeaseName == "" {
		err := errors.New("leader: LeaseName is required")
		logger.Error(err, "leader config invalid")
		return err
	}
	if c.Identity == "" {
		err := errors.New("leader: Identity is required")
		logger.Error(err, "leader config invalid")
		return err
	}
	if c.LeaseDuration <= 0 {
		err := errors.New("leader: LeaseDuration must be positive")
		logger.Error(err, "leader config invalid",
			"leaseDuration", c.LeaseDuration.String())
		return err
	}
	if c.RenewDeadline <= 0 {
		err := errors.New("leader: RenewDeadline must be positive")
		logger.Error(err, "leader config invalid",
			"renewDeadline", c.RenewDeadline.String())
		return err
	}
	if c.RetryPeriod <= 0 {
		err := errors.New("leader: RetryPeriod must be positive")
		logger.Error(err, "leader config invalid",
			"retryPeriod", c.RetryPeriod.String())
		return err
	}
	if c.LeaseDuration <= c.RenewDeadline {
		err := fmt.Errorf("leader: LeaseDuration (%s) must be greater than RenewDeadline (%s)",
			c.LeaseDuration, c.RenewDeadline)
		logger.Error(err, "leader config invalid",
			"leaseDuration", c.LeaseDuration.String(),
			"renewDeadline", c.RenewDeadline.String())
		return err
	}
	if c.RenewDeadline <= c.RetryPeriod {
		err := fmt.Errorf("leader: RenewDeadline (%s) must be greater than RetryPeriod (%s)",
			c.RenewDeadline, c.RetryPeriod)
		logger.Error(err, "leader config invalid",
			"renewDeadline", c.RenewDeadline.String(),
			"retryPeriod", c.RetryPeriod.String())
		return err
	}
	if c.OnStartedLeading == nil {
		err := errors.New("leader: OnStartedLeading callback is required")
		logger.Error(err, "leader config invalid")
		return err
	}
	if c.OnStoppedLeading == nil {
		err := errors.New("leader: OnStoppedLeading callback is required")
		logger.Error(err, "leader config invalid")
		return err
	}
	return nil
}

// Run participates in leader election and blocks until ctx is cancelled or
// the leader elector exits. OnStartedLeading is called with a leader context
// when this instance becomes the leader; OnStoppedLeading is called when
// leadership is lost (including when ctx is cancelled before acquiring).
func Run(ctx context.Context, cfg Config) error {
	logger.Info("starting leader election",
		"namespace", cfg.Namespace,
		"leaseName", cfg.LeaseName,
		"identity", cfg.Identity,
		"leaseDuration", cfg.LeaseDuration.String(),
		"renewDeadline", cfg.RenewDeadline.String(),
		"retryPeriod", cfg.RetryPeriod.String(),
	)
	if err := cfg.Validate(); err != nil {
		return err
	}

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      cfg.LeaseName,
			Namespace: cfg.Namespace,
		},
		Client: cfg.KubeClient.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: cfg.Identity,
		},
	}

	le, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   cfg.LeaseDuration,
		RenewDeadline:   cfg.RenewDeadline,
		RetryPeriod:     cfg.RetryPeriod,
		ReleaseOnCancel: true,
		Name:            cfg.LeaseName,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: cfg.OnStartedLeading,
			OnStoppedLeading: cfg.OnStoppedLeading,
		},
	})
	if err != nil {
		wrapped := fmt.Errorf("leader: create elector: %w", err)
		logger.Error(wrapped, "create leader elector failed",
			"leaseName", cfg.LeaseName, "namespace", cfg.Namespace)
		return wrapped
	}

	le.Run(ctx)
	logger.Info("leader election loop returned",
		"leaseName", cfg.LeaseName,
		"namespace", cfg.Namespace,
		"identity", cfg.Identity,
		"ctxErr", ctx.Err(),
	)
	return nil
}
