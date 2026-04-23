// Package watcher mirrors the lake-watcher convention: per-resource Watch
// types register handlers on shared informers owned by a single Manager, and
// funnel every meaningful change into a policy-keyed workqueue via
// Manager.Enqueue / Manager.EnqueueAfter.
package watcher

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/pzhenzhou/qspill-controller/pkg/common"
)

// logger is the per-module logr.Logger shared by every Watch type, the
// Manager, and the ConfigMap reload path. Module scope keeps lifecycle and
// per-event lines under a single `module=watcher` filter.
var logger = common.NewLogger("watcher")

// convertObj extracts a runtime.Object from a raw informer callback argument,
// handling the cache.DeletedFinalStateUnknown tombstone wrapper that appears
// when an informer's watch stream misses a delete event.
func convertObj(obj interface{}) (runtime.Object, bool) {
	if rObj, ok := obj.(runtime.Object); ok {
		return rObj, true
	}
	tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
	if !ok {
		return nil, false
	}
	rObj, ok := tombstone.Obj.(runtime.Object)
	return rObj, ok
}
