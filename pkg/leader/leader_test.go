package leader

import (
	"context"
	"strings"
	"testing"
	"time"

	kubefake "k8s.io/client-go/kubernetes/fake"
)

func validConfig() Config {
	return Config{
		KubeClient:       kubefake.NewSimpleClientset(),
		Namespace:        "qspill-controller",
		LeaseName:        "qspill-controller",
		Identity:         "pod-0",
		LeaseDuration:    15 * time.Second,
		RenewDeadline:    10 * time.Second,
		RetryPeriod:      2 * time.Second,
		OnStartedLeading: func(context.Context) {},
		OnStoppedLeading: func() {},
	}
}

func TestConfig_Validate_Valid(t *testing.T) {
	c := validConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfig_Validate_NilKubeClient(t *testing.T) {
	c := validConfig()
	c.KubeClient = nil
	assertValidationError(t, c, "KubeClient")
}

func TestConfig_Validate_EmptyNamespace(t *testing.T) {
	c := validConfig()
	c.Namespace = ""
	assertValidationError(t, c, "Namespace")
}

func TestConfig_Validate_EmptyLeaseName(t *testing.T) {
	c := validConfig()
	c.LeaseName = ""
	assertValidationError(t, c, "LeaseName")
}

func TestConfig_Validate_EmptyIdentity(t *testing.T) {
	c := validConfig()
	c.Identity = ""
	assertValidationError(t, c, "Identity")
}

func TestConfig_Validate_ZeroLeaseDuration(t *testing.T) {
	c := validConfig()
	c.LeaseDuration = 0
	assertValidationError(t, c, "LeaseDuration")
}

func TestConfig_Validate_ZeroRenewDeadline(t *testing.T) {
	c := validConfig()
	c.RenewDeadline = 0
	assertValidationError(t, c, "RenewDeadline")
}

func TestConfig_Validate_ZeroRetryPeriod(t *testing.T) {
	c := validConfig()
	c.RetryPeriod = 0
	assertValidationError(t, c, "RetryPeriod")
}

func TestConfig_Validate_LeaseDurationNotGreaterThanRenewDeadline(t *testing.T) {
	c := validConfig()
	c.LeaseDuration = 10 * time.Second
	c.RenewDeadline = 10 * time.Second
	assertValidationError(t, c, "LeaseDuration")
}

func TestConfig_Validate_RenewDeadlineNotGreaterThanRetryPeriod(t *testing.T) {
	c := validConfig()
	c.RenewDeadline = 2 * time.Second
	c.RetryPeriod = 2 * time.Second
	assertValidationError(t, c, "RenewDeadline")
}

func TestConfig_Validate_NilOnStartedLeading(t *testing.T) {
	c := validConfig()
	c.OnStartedLeading = nil
	assertValidationError(t, c, "OnStartedLeading")
}

func TestConfig_Validate_NilOnStoppedLeading(t *testing.T) {
	c := validConfig()
	c.OnStoppedLeading = nil
	assertValidationError(t, c, "OnStoppedLeading")
}

func TestRun_InvalidConfig(t *testing.T) {
	c := validConfig()
	c.KubeClient = nil
	err := Run(context.Background(), c)
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
}

func assertValidationError(t *testing.T, c Config, substring string) {
	t.Helper()
	err := c.Validate()
	if err == nil {
		t.Fatalf("expected validation error containing %q", substring)
	}
	if !strings.Contains(err.Error(), substring) {
		t.Errorf("error %q does not contain %q", err.Error(), substring)
	}
}
