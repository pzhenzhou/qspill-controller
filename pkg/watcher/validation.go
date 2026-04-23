package watcher

import (
	"errors"
	"time"

	"k8s.io/client-go/kubernetes"
	volclient "volcano.sh/apis/pkg/client/clientset/versioned"

	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

func validateKubeClient(c kubernetes.Interface) error {
	if c == nil {
		return errors.New("watcher: kubernetes client is required")
	}
	return nil
}

func validateVolcanoClient(c volclient.Interface) error {
	if c == nil {
		return errors.New("watcher: volcano client is required")
	}
	return nil
}

func validateResyncPeriod(d time.Duration) error {
	if d <= 0 {
		return errors.New("watcher: resync period must be positive")
	}
	return nil
}

func validateRegistryStore(s *config.RegistryStore) error {
	if s == nil {
		return errors.New("watcher: registry store is required")
	}
	return nil
}
