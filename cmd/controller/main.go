// Command qspill-controller is the entrypoint for the controller binary.
// It builds Kubernetes clients, loads configuration, wires the reconcile
// pipeline, starts a /healthz probe server, and enters leader-elected
// operation with graceful SIGTERM handling.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	volclient "volcano.sh/apis/pkg/client/clientset/versioned"

	"github.com/pzhenzhou/qspill-controller/pkg/action"
	"github.com/pzhenzhou/qspill-controller/pkg/api"
	"github.com/pzhenzhou/qspill-controller/pkg/common"
	"github.com/pzhenzhou/qspill-controller/pkg/config"
	"github.com/pzhenzhou/qspill-controller/pkg/evaluator"
	"github.com/pzhenzhou/qspill-controller/pkg/leader"
	"github.com/pzhenzhou/qspill-controller/pkg/reconcile"
	"github.com/pzhenzhou/qspill-controller/pkg/snapshot"
	"github.com/pzhenzhou/qspill-controller/pkg/watcher"
)

var (
	version   = "v0.0.0-dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// logger is the per-module logr.Logger for the binary entry point. Lines are
// tagged `module=main` so operators can tell process bootstrap and shutdown
// events apart from controller traffic.
var logger = common.NewLogger("main")

// fatal logs an error with module=main and exits 1. Used in place of
// zap.Logger.Fatal so the binary stays on a single logr-based logging stack.
func fatal(err error, msg string, kv ...any) {
	logger.Error(err, msg, kv...)
	_ = common.Logger().Sync()
	os.Exit(1)
}

func main() {
	var (
		showVersion bool
		configPath  string
		probeAddr   string
		namespace   string
		configName  string
		kubeconfig  string
	)
	flag.BoolVar(&showVersion, "version", false, "print version information and exit")
	flag.StringVar(&configPath, "config", "/etc/qspill-controller/config.yaml",
		"path to the controller configuration file")
	flag.StringVar(&probeAddr, "probe-addr", ":8081",
		"address the /healthz endpoint binds to")
	flag.StringVar(&namespace, "namespace", "qspill-controller",
		"namespace for leader election lease and ConfigMap watch")
	flag.StringVar(&configName, "config-name", "qspill-controller-config",
		"name of the ConfigMap to watch for configuration reloads")
	flag.StringVar(&kubeconfig, "kubeconfig", "",
		"path to kubeconfig file (defaults to in-cluster config)")
	flag.Parse()

	if showVersion {
		fmt.Printf("qspill-controller %s\ncommit:    %s\nbuilt:     %s\ngo:        %s\nplatform:  %s/%s\n",
			version, commit, buildDate, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return
	}

	common.InitLogger()
	defer func() { _ = common.Logger().Sync() }()

	logger.Info("qspill-controller starting",
		"version", version,
		"commit", commit,
		"buildDate", buildDate,
		"config", configPath,
		"probeAddr", probeAddr,
		"namespace", namespace,
		"configName", configName,
	)

	// 1. Load configuration: set env so config.Load picks up the file.
	logger.Info("loading configuration", "config", configPath)
	if err := os.Setenv(config.EnvConfigFile, configPath); err != nil {
		fatal(err, "failed to set config env", "envVar", config.EnvConfigFile, "value", configPath)
	}
	registry, err := config.Load()
	if err != nil {
		fatal(err, "failed to load configuration", "config", configPath)
	}
	registryStore := config.NewRegistryStore()
	registryStore.Set(registry)

	defaults := registry.Defaults()
	logger.Info("configuration loaded",
		"action", string(defaults.Action),
		"nodeGroupLabelKey", defaults.NodeGroupLabelKey,
		"reconcileResyncPeriod", defaults.ReconcileResyncPeriod.String(),
		"policies", len(registry.Policies()),
	)

	// 2. Build Kubernetes clients.
	logger.Info("building kubernetes clients", "kubeconfig", kubeconfig)
	restCfg, err := buildK8sConfig(kubeconfig)
	if err != nil {
		fatal(err, "failed to build rest config", "kubeconfig", kubeconfig)
	}
	kubeClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		fatal(err, "failed to build kubernetes client")
	}
	volcanoClient, err := volclient.NewForConfig(restCfg)
	if err != nil {
		fatal(err, "failed to build volcano client")
	}

	// 3. Select action implementation.
	var act action.Action
	switch defaults.Action {
	case api.ActionModePatch:
		act = action.NewPatch(volcanoClient)
	default:
		act = action.NewNope(nil)
	}
	logger.Info("action selected", "action", act.Name(), "configured", string(defaults.Action))

	// 4. Build ReconcilerFactory — the reconciler is assembled during
	// Manager.Start once informer-backed listers are available.
	reconcilerFactory := func(listers snapshot.Listers) reconcile.Reconciler {
		builder := snapshot.NewBuilder(registryStore.Get(), listers)
		eval := evaluator.New()
		return reconcile.New(builder, eval, act, nil)
	}

	// 5. Set up watcher components.
	logger.Info("setting up watcher components",
		"namespace", namespace,
		"configName", configName,
		"resyncPeriod", "30s",
		"reconcileResyncPeriod", defaults.ReconcileResyncPeriod.String(),
		"nodeGroupLabelKey", defaults.NodeGroupLabelKey,
	)
	watcherComponents, err := watcher.SetUpWatcher(
		kubeClient,
		volcanoClient,
		registryStore,
		reconcilerFactory,
		30*time.Second,
		defaults.ReconcileResyncPeriod,
		defaults.NodeGroupLabelKey,
		namespace,
		configName,
	)
	if err != nil {
		fatal(err, "failed to set up watcher",
			"namespace", namespace, "configName", configName)
	}

	// 6. Start /healthz probe server.
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	healthServer := &http.Server{Addr: probeAddr, Handler: healthMux}
	go func() {
		logger.Info("healthz server starting", "addr", probeAddr)
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(err, "healthz server error", "addr", probeAddr)
		}
	}()

	// 7. Signal handling: cancel root context on SIGINT/SIGTERM.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 8. Leader election: OnStartedLeading starts the watcher, OnStoppedLeading
	// triggers graceful shutdown.
	hostname, err := os.Hostname()
	if err != nil {
		fatal(err, "failed to get hostname for leader identity")
	}

	leCfg := leader.Config{
		KubeClient:    kubeClient,
		Namespace:     namespace,
		LeaseName:     "qspill-controller",
		Identity:      hostname,
		LeaseDuration: 15 * time.Second,
		RenewDeadline: 10 * time.Second,
		RetryPeriod:   2 * time.Second,
		OnStartedLeading: func(leaderCtx context.Context) {
			logger.Info("leader election won, starting watcher manager",
				"identity", hostname, "lease", "qspill-controller", "namespace", namespace)
			if err := watcherComponents.Start(leaderCtx); err != nil {
				fatal(err, "watcher manager failed to start",
					"identity", hostname, "namespace", namespace)
			}
			logger.Info("watcher manager started, informers synced",
				"identity", hostname)
			<-leaderCtx.Done()
			logger.Info("leader context cancelled, OnStartedLeading returning",
				"identity", hostname, "ctxErr", leaderCtx.Err())
		},
		OnStoppedLeading: func() {
			logger.Info("leader election lost, shutting down",
				"identity", hostname, "namespace", namespace)
			if err := watcherComponents.GracefulShutdown(30 * time.Second); err != nil {
				logger.Error(err, "graceful shutdown error", "identity", hostname)
			}
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			if err := healthServer.Shutdown(shutdownCtx); err != nil {
				logger.Error(err, "healthz server shutdown error", "addr", probeAddr)
			}
			logger.Info("shutdown complete, exiting", "identity", hostname)
			_ = common.Logger().Sync()
			os.Exit(0)
		},
	}

	logger.Info("entering leader election",
		"identity", hostname,
		"lease", leCfg.LeaseName,
		"namespace", namespace,
		"leaseDuration", leCfg.LeaseDuration.String(),
		"renewDeadline", leCfg.RenewDeadline.String(),
		"retryPeriod", leCfg.RetryPeriod.String(),
	)
	if err := leader.Run(ctx, leCfg); err != nil {
		fatal(err, "leader election failed",
			"identity", hostname, "lease", leCfg.LeaseName, "namespace", namespace)
	}

	logger.Info("leader.Run returned, process exiting",
		"identity", hostname, "ctxErr", ctx.Err())
}

// buildK8sConfig returns a rest.Config using in-cluster configuration when
// available, falling back to the kubeconfig file path (useful for local
// development).
func buildK8sConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			logger.Error(err, "build rest config from kubeconfig failed",
				"kubeconfig", kubeconfig)
			return nil, err
		}
		return cfg, nil
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		home, _ := os.UserHomeDir()
		defaultKubeconfig := home + "/.kube/config"
		logger.Info("in-cluster config unavailable, falling back to kubeconfig",
			"reason", err.Error(), "kubeconfig", defaultKubeconfig)
		fallback, ferr := clientcmd.BuildConfigFromFlags("", defaultKubeconfig)
		if ferr != nil {
			logger.Error(ferr, "build rest config from default kubeconfig failed",
				"kubeconfig", defaultKubeconfig)
			return nil, ferr
		}
		return fallback, nil
	}
	return cfg, nil
}
