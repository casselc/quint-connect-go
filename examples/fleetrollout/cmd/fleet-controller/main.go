// Command fleet-controller runs the rollout controller against real member
// clusters through multicluster-runtime: one controller watches the
// Deployment in every member, and every event reconciles the whole fleet.
// Reads go to each member's API server (uncached); the model steps go to a
// step log.
//
//	fleet-controller -member m1=~/.kwok/clusters/m1/kubeconfig.yaml -member m2=... -steps steps.jsonl
//
// With -verify it does not reconcile: it reads every member once (observe
// steps), records settle, and exits: the end of a run, for checking
// rolledOutWhenSettled.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/providers/clusters"

	"github.com/casselc/quint-connect-go/examples/fleetrollout/rollout"
	"github.com/casselc/quint-connect-go/qobs"
)

type members map[string]string

func (m members) String() string { return fmt.Sprint(map[string]string(m)) }
func (m members) Set(v string) error {
	name, path, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("want NAME=KUBECONFIG, got %q", v)
	}
	m[name] = path
	return nil
}

// fleet implements rollout.Clusters over a multicluster-runtime manager.
type fleet struct {
	mgr   mcmanager.Manager
	names []string
}

func (f fleet) Names() []string { return f.names }

func (f fleet) Reader(ctx context.Context, name string) (client.Reader, error) {
	cl, err := f.mgr.GetCluster(ctx, multicluster.ClusterName(name))
	if err != nil {
		return nil, err
	}
	return cl.GetAPIReader(), nil // uncached: the controller decides on fresh reads
}

func (f fleet) Writer(ctx context.Context, name string) (client.Writer, error) {
	cl, err := f.mgr.GetCluster(ctx, multicluster.ClusterName(name))
	if err != nil {
		return nil, err
	}
	return cl.GetClient(), nil
}

func main() {
	ms := members{}
	flag.Var(ms, "member", "NAME=KUBECONFIG of a member cluster (repeat)")
	image := flag.String("image", "web:v2", "target image")
	maxUnavailable := flag.Int("max-unavailable", 1, "clusters in progress at once")
	mutation := flag.String("mutation", "", "ignore-observed-generation | in-memory-progress")
	steps := flag.String("steps", "steps.jsonl", "step log")
	verify := flag.Bool("verify", false, "read every member, record settle, exit")
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	if err := run(ms, *image, *maxUnavailable, *mutation, *steps, *verify); err != nil {
		fmt.Fprintln(os.Stderr, "fleet-controller:", err)
		os.Exit(1)
	}
}

func run(ms members, image string, maxUnavailable int, mutation, steps string, verify bool) error {
	if len(ms) == 0 {
		return fmt.Errorf("no -member")
	}
	sink, err := qobs.DurableFileSink(steps) // a killed controller loses no steps
	if err != nil {
		return err
	}
	defer sink.Close()
	qobs.SetGlobal(qobs.NewRecorder(sink, qobs.WithActor("fleet")))

	var names []string
	for n := range ms {
		names = append(names, n)
	}
	sort.Strings(names)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	provider := clusters.New()
	var local = ms[names[0]]
	localCfg, err := clientcmd.BuildConfigFromFlags("", local)
	if err != nil {
		return err
	}
	mgr, err := mcmanager.New(localCfg, provider, manager.Options{Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		return err
	}
	for _, n := range names {
		cfg, err := clientcmd.BuildConfigFromFlags("", ms[n])
		if err != nil {
			return err
		}
		cl, err := cluster.New(cfg)
		if err != nil {
			return err
		}
		if err := provider.Add(ctx, multicluster.ClusterName(n), cl); err != nil {
			return err
		}
	}
	ctl := &rollout.Controller{
		Clusters: fleet{mgr: mgr, names: names}, Key: client.ObjectKey{Namespace: "default", Name: "web"},
		Image: image, MaxUnavailable: maxUnavailable, RecordPatches: true,
		Mutation: map[string]rollout.Mutation{"": rollout.Faithful, "ignore-observed-generation": rollout.IgnoreObservedGeneration,
			"in-memory-progress": rollout.InMemoryProgress}[mutation],
	}
	if verify {
		return verifyFleet(ctx, mgr, ctl, names)
	}
	err = mcbuilder.ControllerManagedBy(mgr).Named("fleet-rollout").For(&appsv1.Deployment{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			if req.Name != ctl.Key.Name || req.Namespace != ctl.Key.Namespace {
				return ctrl.Result{}, nil
			}
			done, err := ctl.Reconcile(ctx)
			ctrl.Log.V(1).Info("reconciled", "trigger", req.String(), "done", done)
			if err != nil {
				ctrl.Log.Info("reconcile failed; retrying", "error", err.Error())
			}
			if err != nil || !done {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, nil
		}))
	if err != nil {
		return err
	}
	return mgr.Start(ctx)
}

// verifyFleet reads every member once, after the manager has engaged them.
func verifyFleet(ctx context.Context, mgr mcmanager.Manager, ctl *rollout.Controller, names []string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	go func() { _ = mgr.Start(ctx) }()
	for _, n := range names {
		var cl cluster.Cluster
		var err error
		for cl == nil {
			if cl, err = mgr.GetCluster(ctx, multicluster.ClusterName(n)); err != nil {
				select {
				case <-ctx.Done():
					return fmt.Errorf("%s: %w", n, err)
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
		d := &appsv1.Deployment{}
		if err := cl.GetAPIReader().Get(ctx, ctl.Key, d); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		ctl.Observe(ctx, n, d)
		fmt.Printf("%s: %s generation %d observed %d updated %d/%d available %d\n", n, rollout.Image(d), d.Generation,
			d.Status.ObservedGeneration, d.Status.UpdatedReplicas, *d.Spec.Replicas, d.Status.AvailableReplicas)
	}
	qobs.Record(ctx, "settle", nil)
	return nil
}
