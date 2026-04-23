package watcher

import (
	"context"
	"time"

	"k8s.io/client-go/kubernetes"
	volclient "volcano.sh/apis/pkg/client/clientset/versioned"

	"github.com/pzhenzhou/qspill-controller/pkg/config"
	"github.com/pzhenzhou/qspill-controller/pkg/snapshot"
)

// WatcherComponents bundles the Manager with its lifecycle helpers.
type WatcherComponents struct {
	Manager          *Manager
	Start            func(ctx context.Context) error
	GracefulShutdown func(timeout time.Duration) error
	Listers          func() snapshot.Listers
}

// SetUpWatcher constructs the full Manager and returns lifecycle callbacks.
// The reconcilerFactory is invoked during Start once informer-backed listers
// are available.
func SetUpWatcher(
	kubeClient kubernetes.Interface,
	volcanoClient volclient.Interface,
	registryStore *config.RegistryStore,
	reconcilerFactory ReconcilerFactory,
	resyncPeriod time.Duration,
	reconcileResyncPeriod time.Duration,
	nodeGroupKey string,
	namespace string,
	configName string,
) (*WatcherComponents, error) {
	mgr, err := NewManager(
		kubeClient,
		volcanoClient,
		registryStore,
		reconcilerFactory,
		resyncPeriod,
		reconcileResyncPeriod,
		nodeGroupKey,
		namespace,
		configName,
	)
	if err != nil {
		return nil, err
	}

	return &WatcherComponents{
		Manager:          mgr,
		Start:            mgr.Start,
		GracefulShutdown: mgr.Stop,
		Listers:          mgr.Listers,
	}, nil
}
