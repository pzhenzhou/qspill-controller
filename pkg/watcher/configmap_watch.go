package watcher

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/pzhenzhou/qspill-controller/pkg/config"
)

const configDataKey = "config.yaml"

// ConfigMapWatch reloads configuration when the controller's ConfigMap
// changes, atomically swapping the registry and enqueuing all affected
// policies. See DESIGN.md §6.5.
type ConfigMapWatch struct {
	registryStore *config.RegistryStore
	enqueue       func(policyName string)
}

func newConfigMapWatch(
	kubeClient kubernetes.Interface,
	registryStore *config.RegistryStore,
	resyncPeriod time.Duration,
	namespace string,
	configName string,
	enqueue func(string),
) (*ConfigMapWatch, cache.SharedIndexInformer, cache.ResourceEventHandlerRegistration, error) {
	w := &ConfigMapWatch{
		registryStore: registryStore,
		enqueue:       enqueue,
	}

	fieldSelector := fields.OneTermEqualSelector("metadata.name", configName).String()

	informer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
				options.FieldSelector = fieldSelector
				return kubeClient.CoreV1().ConfigMaps(namespace).List(ctx, options)
			},
			WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
				options.FieldSelector = fieldSelector
				return kubeClient.CoreV1().ConfigMaps(namespace).Watch(ctx, options)
			},
		},
		&corev1.ConfigMap{},
		resyncPeriod,
		cache.Indexers{},
	)

	reg, err := informer.AddEventHandler(cache.ResourceEventHandlerDetailedFuncs{
		AddFunc:    w.onAdd,
		UpdateFunc: w.onUpdate,
		DeleteFunc: nil,
	})
	if err != nil {
		logger.Error(err, "configmap watch: add event handler failed",
			"namespace", namespace, "configName", configName)
		return nil, nil, nil, err
	}

	return w, informer, reg, nil
}

func (w *ConfigMapWatch) onAdd(obj interface{}, _ bool) {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", obj),
			"configmap add: type assertion failed")
		return
	}
	w.reload(cm)
}

func (w *ConfigMapWatch) onUpdate(_, newObj interface{}) {
	cm, ok := newObj.(*corev1.ConfigMap)
	if !ok {
		logger.Error(fmt.Errorf("unexpected type %T", newObj),
			"configmap update: type assertion failed")
		return
	}
	w.reload(cm)
}

func (w *ConfigMapWatch) reload(cm *corev1.ConfigMap) {
	logger.Info("reloading configuration from ConfigMap",
		"namespace", cm.Namespace,
		"name", cm.Name,
		"resourceVersion", cm.ResourceVersion,
		"dataKey", configDataKey,
	)

	data := []byte(cm.Data[configDataKey])
	newReg, err := config.LoadFromBytes(data)
	if err != nil {
		logger.Error(err, "configmap reload failed; keeping previous registry",
			"namespace", cm.Namespace,
			"name", cm.Name,
			"resourceVersion", cm.ResourceVersion,
			"bytes", len(data),
		)
		return
	}

	oldReg := w.registryStore.Get()
	w.registryStore.Set(newReg)

	seen := make(map[string]struct{})
	for _, p := range oldReg.Policies() {
		if _, dup := seen[p.Name]; !dup {
			seen[p.Name] = struct{}{}
			w.enqueue(p.Name)
		}
	}
	for _, p := range newReg.Policies() {
		if _, dup := seen[p.Name]; !dup {
			seen[p.Name] = struct{}{}
			w.enqueue(p.Name)
		}
	}

	logger.Info("configmap reload applied",
		"namespace", cm.Namespace,
		"name", cm.Name,
		"oldPolicies", len(oldReg.Policies()),
		"newPolicies", len(newReg.Policies()),
		"reconciledPolicies", len(seen),
	)
}
