package watcher

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	volclient "volcano.sh/apis/pkg/client/clientset/versioned"

	"github.com/pzhenzhou/qspill-controller/pkg/config"
	"github.com/pzhenzhou/qspill-controller/pkg/reconcile"
	"github.com/pzhenzhou/qspill-controller/pkg/snapshot"
)

// ReconcilerFactory builds a Reconciler from the informer-backed Listers
// that only become available after registerWatches. This breaks the
// chicken-and-egg dependency: main.go defines how the reconciler is
// assembled, while the Manager controls when the informers are ready.
type ReconcilerFactory func(snapshot.Listers) reconcile.Reconciler

// Manager owns the shared informers, workqueue, and worker goroutine. All
// per-resource Watch types register handlers on its informers and funnel
// changes through Enqueue/EnqueueAfter.
type Manager struct {
	kubeClient    kubernetes.Interface
	volcanoClient volclient.Interface
	registryStore *config.RegistryStore

	reconcilerFactory     ReconcilerFactory
	reconciler            reconcile.Reconciler
	resyncPeriod          time.Duration
	reconcileResyncPeriod time.Duration
	nodeGroupKey          string
	namespace             string
	configName            string

	workqueue   workqueue.TypedRateLimitingInterface[string]
	pendingWork *PendingWorkMap

	stopCh   chan struct{}
	inFlight atomic.Int32

	informers   []cache.SharedIndexInformer
	handlerRegs []cache.ResourceEventHandlerRegistration

	pgInformer    cache.SharedIndexInformer
	nodeInformer  cache.SharedIndexInformer
	podInformer   cache.SharedIndexInformer
	eventInformer cache.SharedIndexInformer
	queueInformer cache.SharedIndexInformer
}

// NewManager constructs a Manager with validated dependencies. The
// reconcilerFactory is called during Start once informer-backed listers are
// available.
func NewManager(
	kubeClient kubernetes.Interface,
	volcanoClient volclient.Interface,
	registryStore *config.RegistryStore,
	reconcilerFactory ReconcilerFactory,
	resyncPeriod time.Duration,
	reconcileResyncPeriod time.Duration,
	nodeGroupKey string,
	namespace string,
	configName string,
) (*Manager, error) {
	logger.Info("constructing watcher manager",
		"namespace", namespace,
		"configName", configName,
		"nodeGroupKey", nodeGroupKey,
		"resyncPeriod", resyncPeriod.String(),
		"reconcileResyncPeriod", reconcileResyncPeriod.String(),
	)
	if err := validateKubeClient(kubeClient); err != nil {
		logger.Error(err, "validate kube client failed")
		return nil, err
	}
	if err := validateVolcanoClient(volcanoClient); err != nil {
		logger.Error(err, "validate volcano client failed")
		return nil, err
	}
	if err := validateRegistryStore(registryStore); err != nil {
		logger.Error(err, "validate registry store failed")
		return nil, err
	}
	if reconcilerFactory == nil {
		err := fmt.Errorf("watcher: reconciler factory is required")
		logger.Error(err, "validate reconciler factory failed")
		return nil, err
	}
	if err := validateResyncPeriod(resyncPeriod); err != nil {
		logger.Error(err, "validate resync period failed",
			"resyncPeriod", resyncPeriod.String())
		return nil, err
	}
	if reconcileResyncPeriod <= 0 {
		err := fmt.Errorf("watcher: reconcile resync period must be positive")
		logger.Error(err, "validate reconcile resync period failed",
			"reconcileResyncPeriod", reconcileResyncPeriod.String())
		return nil, err
	}
	if nodeGroupKey == "" {
		err := fmt.Errorf("watcher: node group label key is required")
		logger.Error(err, "validate node group key failed")
		return nil, err
	}
	if namespace == "" {
		err := fmt.Errorf("watcher: namespace is required")
		logger.Error(err, "validate namespace failed")
		return nil, err
	}
	if configName == "" {
		err := fmt.Errorf("watcher: config name is required")
		logger.Error(err, "validate config name failed")
		return nil, err
	}

	return &Manager{
		kubeClient:            kubeClient,
		volcanoClient:         volcanoClient,
		registryStore:         registryStore,
		reconcilerFactory:     reconcilerFactory,
		resyncPeriod:          resyncPeriod,
		reconcileResyncPeriod: reconcileResyncPeriod,
		nodeGroupKey:          nodeGroupKey,
		namespace:             namespace,
		configName:            configName,
		workqueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "qspill-controller"},
		),
		pendingWork: NewPendingWorkMap(),
		stopCh:      make(chan struct{}),
	}, nil
}

// Enqueue adds a policy name to the workqueue for reconciliation. Safe to
// call from any goroutine (including informer event handlers).
func (m *Manager) Enqueue(policyName string) {
	m.workqueue.Add(policyName)
}

// EnqueueAfter schedules a deferred reconciliation. The PendingWorkMap
// coalesces duplicate requests so only the earliest deadline actually fires.
func (m *Manager) EnqueueAfter(policyName string, d time.Duration) {
	deadline := time.Now().Add(d)
	if m.pendingWork.ShouldEnqueueAfter(policyName, deadline) {
		logger.Info("scheduling deferred reconcile",
			"policy", policyName,
			"after", d.String(),
			"deadline", deadline.UTC().Format(time.RFC3339),
		)
		m.workqueue.AddAfter(policyName, d)
	}
}

// Start creates informers, registers all watches, builds the reconciler
// from the factory, starts the informers, waits for cache sync, and
// launches the worker goroutine and forced periodic resync. It blocks
// until caches are synced or ctx is cancelled.
func (m *Manager) Start(ctx context.Context) error {
	logger.Info("starting watcher manager",
		"namespace", m.namespace,
		"configName", m.configName,
		"resyncPeriod", m.resyncPeriod.String(),
		"reconcileResyncPeriod", m.reconcileResyncPeriod.String(),
	)
	if err := m.registerWatches(); err != nil {
		wrapped := fmt.Errorf("watcher: register watches: %w", err)
		logger.Error(wrapped, "register watches failed")
		return wrapped
	}

	listers := NewListersFromInformers(
		m.pgInformer, m.nodeInformer, m.podInformer,
		m.eventInformer, m.queueInformer,
	)
	m.reconciler = m.reconcilerFactory(listers)

	logger.Info("starting informers and waiting for cache sync",
		"informers", len(m.informers),
	)
	for _, inf := range m.informers {
		go inf.Run(m.stopCh)
	}

	syncFns := make([]cache.InformerSynced, len(m.informers))
	for i, inf := range m.informers {
		syncFns[i] = inf.HasSynced
	}
	if !cache.WaitForCacheSync(ctx.Done(), syncFns...) {
		err := fmt.Errorf("watcher: timed out waiting for cache sync")
		logger.Error(err, "cache sync timed out", "informers", len(m.informers))
		return err
	}

	logger.Info("informer caches synced; launching worker and periodic resync",
		"informers", len(m.informers),
	)
	go m.runWorker(ctx)
	go m.runPeriodicResync(ctx)
	return nil
}

// Listers returns the informer-backed listers. Only valid after Start
// returns successfully.
func (m *Manager) Listers() snapshot.Listers {
	return NewListersFromInformers(
		m.pgInformer, m.nodeInformer, m.podInformer,
		m.eventInformer, m.queueInformer,
	)
}

// Stop shuts down the workqueue and waits for the in-flight item to drain
// or the timeout to expire, whichever comes first.
func (m *Manager) Stop(timeout time.Duration) error {
	logger.Info("stopping watcher manager",
		"timeout", timeout.String(),
		"inFlight", m.inFlight.Load(),
		"queueLen", m.workqueue.Len(),
	)
	close(m.stopCh)
	m.workqueue.ShutDown()

	deadline := time.After(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			err := fmt.Errorf("watcher: stop timed out with %d in-flight, %d queued",
				m.inFlight.Load(), m.workqueue.Len())
			logger.Error(err, "graceful stop timed out",
				"timeout", timeout.String(),
				"inFlight", m.inFlight.Load(),
				"queueLen", m.workqueue.Len(),
			)
			return err
		case <-ticker.C:
			if m.inFlight.Load() == 0 && m.workqueue.Len() == 0 {
				logger.Info("watcher manager stopped cleanly")
				return nil
			}
		}
	}
}

// InFlight returns the number of items currently being processed.
func (m *Manager) InFlight() int32 {
	return m.inFlight.Load()
}

// QueueLen returns the number of items waiting in the workqueue.
func (m *Manager) QueueLen() int {
	return m.workqueue.Len()
}

func (m *Manager) runWorker(ctx context.Context) {
	for m.processNextItem(ctx) {
	}
}

// runPeriodicResync re-enqueues every policy in the registry at the
// configured reconcileResyncPeriod, providing a self-healing floor for
// missed watch events (DESIGN.md §10.5).
func (m *Manager) runPeriodicResync(ctx context.Context) {
	logger.Info("forced periodic resync started",
		"period", m.reconcileResyncPeriod.String(),
	)
	ticker := time.NewTicker(m.reconcileResyncPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("forced periodic resync stopping", "reason", "context done")
			return
		case <-m.stopCh:
			logger.Info("forced periodic resync stopping", "reason", "stop channel closed")
			return
		case <-ticker.C:
			policies := m.registryStore.Get().Policies()
			logger.Info("forced periodic resync tick",
				"policies", len(policies),
			)
			for _, p := range policies {
				m.Enqueue(p.Name)
			}
		}
	}
}

func (m *Manager) processNextItem(ctx context.Context) bool {
	key, shutdown := m.workqueue.Get()
	if shutdown {
		logger.Info("worker exiting", "reason", "workqueue shutdown")
		return false
	}
	defer m.workqueue.Done(key)

	logger.Info("processing workqueue item",
		"policy", key,
		"queueLen", m.workqueue.Len(),
	)

	m.pendingWork.Clear(key)
	m.inFlight.Add(1)
	defer m.inFlight.Add(-1)

	if err := m.reconciler.ReconcilePolicy(ctx, key); err != nil {
		if errors.Is(err, snapshot.ErrUnknownPolicy) {
			logger.Info("dropping workqueue item for unknown policy",
				"policy", key,
				"reason", err.Error(),
			)
			m.workqueue.Forget(key)
			return true
		}
		logger.Error(err, "reconcile failed; rate-limited requeue",
			"policy", key,
		)
		m.workqueue.AddRateLimited(key)
		return true
	}
	m.workqueue.Forget(key)
	return true
}

// registerWatches is called once during Start. It builds informers and
// registers all per-resource watch handlers. It also stores each informer
// reference on the Manager so Listers() can build snapshot.Listers from
// them after cache sync.
func (m *Manager) registerWatches() error {
	logger.Info("registering watches",
		"resyncPeriod", m.resyncPeriod.String(),
		"nodeGroupKey", m.nodeGroupKey,
		"namespace", m.namespace,
		"configName", m.configName,
	)

	pgW, pgInf, pgReg, err := newPodGroupWatch(m.volcanoClient, m.registryStore, m.resyncPeriod, m.Enqueue)
	if err != nil {
		logger.Error(err, "register podgroup watch failed")
		return err
	}
	m.pgInformer = pgInf
	m.informers = append(m.informers, pgInf)
	m.handlerRegs = append(m.handlerRegs, pgReg)
	_ = pgW

	nodeW, nodeInf, nodeReg, err := newNodeWatch(m.kubeClient, m.registryStore, m.resyncPeriod, m.nodeGroupKey, m.Enqueue)
	if err != nil {
		logger.Error(err, "register node watch failed",
			"nodeGroupKey", m.nodeGroupKey)
		return err
	}
	m.nodeInformer = nodeInf
	m.informers = append(m.informers, nodeInf)
	m.handlerRegs = append(m.handlerRegs, nodeReg)
	_ = nodeW

	peW, podInf, eventInf, podReg, eventReg, err := newPodEventWatch(
		m.kubeClient, m.registryStore, m.pgInformer, m.resyncPeriod, m.Enqueue, m.EnqueueAfter,
	)
	if err != nil {
		logger.Error(err, "register pod/event watch failed")
		return err
	}
	m.podInformer = podInf
	m.eventInformer = eventInf
	m.informers = append(m.informers, podInf, eventInf)
	m.handlerRegs = append(m.handlerRegs, podReg, eventReg)
	_ = peW

	qW, qInf, qReg, err := newQueueWatch(m.volcanoClient, m.registryStore, m.resyncPeriod, m.Enqueue)
	if err != nil {
		logger.Error(err, "register queue watch failed")
		return err
	}
	m.queueInformer = qInf
	m.informers = append(m.informers, qInf)
	m.handlerRegs = append(m.handlerRegs, qReg)
	_ = qW

	cmW, cmInf, cmReg, err := newConfigMapWatch(
		m.kubeClient, m.registryStore, m.resyncPeriod,
		m.namespace, m.configName, m.Enqueue,
	)
	if err != nil {
		logger.Error(err, "register configmap watch failed",
			"namespace", m.namespace, "configName", m.configName)
		return err
	}
	m.informers = append(m.informers, cmInf)
	m.handlerRegs = append(m.handlerRegs, cmReg)
	_ = cmW

	logger.Info("watches registered",
		"informers", len(m.informers),
	)
	return nil
}
