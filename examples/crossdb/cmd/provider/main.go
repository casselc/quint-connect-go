// Command provider runs the dbaas provider against a cluster (out of
// cluster, as `make run` does for real providers): crossplane-runtime's
// managed reconciler for Database resources, talking to a dbaas server.
//
//	provider -kubeconfig ~/.kube/config -dbaas http://localhost:8085 [-mutation no-client-token]
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/casselc/quint-connect-go/examples/crossdb"
	"github.com/casselc/quint-connect-go/examples/crossdb/apis/v1alpha1"
	"github.com/casselc/quint-connect-go/examples/crossdb/dbaas"
	"github.com/casselc/quint-connect-go/examples/crossdb/provider"
)

func main() {
	url := flag.String("dbaas", "http://localhost:8085", "dbaas server")
	mutation := flag.String("mutation", "", "generated-external-name | no-client-token | password-from-create")
	poll := flag.Duration("poll", 10*time.Second, "poll interval")
	flag.Parse()
	log := zap.New(zap.UseDevMode(false))
	ctrl.SetLogger(log)
	cfg, err := ctrl.GetConfig() // -kubeconfig, $KUBECONFIG, or in-cluster
	if err != nil {
		fail(err)
	}
	mgr, err := manager.New(cfg, manager.Options{Scheme: crossdb.Scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		fail(err)
	}
	m := map[string]provider.Mutation{"": provider.Faithful, "generated-external-name": provider.GeneratedExternalName,
		"no-client-token": provider.NoClientToken, "password-from-create": provider.PasswordFromCreate}[*mutation]
	api := &dbaas.Client{URL: *url}
	opts := append(provider.ReconcilerOptions(mgr.GetClient(), api, provider.Options{Mutation: m, PollInterval: *poll}),
		managed.WithLogger(logging.NewLogrLogger(log.WithName("dbaas"))))
	r := managed.NewReconciler(mgr, resource.ManagedKind(v1alpha1.DatabaseGVK), opts...)
	err = ctrl.NewControllerManagedBy(mgr).Named("database").For(&v1alpha1.Database{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).Complete(r)
	if err != nil {
		fail(err)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "provider:", err)
	os.Exit(1)
}
