// Command driver plays the platform's users against a real cluster with
// Crossplane: it creates AppDatabases, records each composed Database as
// it appears, deletes some of them at random times, waits for the system to
// go quiet, then audits every Database and its connection secret and
// records settle. Its steps carry a wall-clock order (order.wall), as the
// dbaas server's do, so the two step logs merge.
//
//	driver -n 4 -steps driver.jsonl -seed 1 -wait 3m
//	driver -cleanup
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/casselc/quint-connect-go/examples/crossdb"
	"github.com/casselc/quint-connect-go/examples/crossdb/apis/v1alpha1"
	"github.com/casselc/quint-connect-go/qobs"
)

var appDatabase = schema.GroupVersionKind{Group: "platform.example.org", Version: "v1alpha1", Kind: "AppDatabase"}

const ns = "default"

// wall is the step fields of a driver thread: the thread (one per
// goroutine: program order holds only within one), and the wall clock.
func wall(thread string) []any {
	return []any{"thread", "driver/" + thread, "order.wall", time.Now().UnixNano()}
}

func main() {
	n := flag.Int("n", 4, "AppDatabases to create")
	steps := flag.String("steps", "driver.jsonl", "step log")
	seed := flag.Uint64("seed", 1, "seed for which to delete, and when")
	wait := flag.Duration("wait", 3*time.Minute, "how long to wait for quiescence")
	cleanup := flag.Bool("cleanup", false, "delete every AppDatabase and Database (removing finalizers) and exit")
	flag.Parse()
	cfg, err := ctrl.GetConfig()
	if err != nil {
		fail(err)
	}
	kube, err := client.New(cfg, client.Options{Scheme: crossdb.Scheme})
	if err != nil {
		fail(err)
	}
	ctx := context.Background()
	if *cleanup {
		clean(ctx, kube)
		return
	}
	sink, err := qobs.DurableFileSink(*steps)
	if err != nil {
		fail(err)
	}
	defer sink.Close()
	qobs.SetGlobal(qobs.NewRecorder(sink, qobs.WithActor("crossdb"), qobs.WithProcess("crossdb")))
	rnd := rand.New(rand.NewPCG(*seed, 11))

	type res struct {
		xr, mr, conn string
		deleted      bool
	}
	all := make([]*res, *n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range all {
		r := &res{xr: fmt.Sprintf("app%d", i+1)}
		all[i] = r
		del := rnd.IntN(2) == 0
		delay := time.Duration(rnd.IntN(8)) * 500 * time.Millisecond
		wg.Add(1)
		go func() {
			defer wg.Done()
			xr := &unstructured.Unstructured{}
			xr.SetGroupVersionKind(appDatabase)
			xr.SetNamespace(ns)
			xr.SetName(r.xr)
			_ = unstructured.SetNestedField(xr.Object, "postgres", "spec", "engine")
			if err := kube.Create(ctx, xr); err != nil {
				fail(err)
			}
			mr := composed(ctx, kube, r.xr)
			mu.Lock()
			r.mr, r.conn = mr, r.xr+"-conn"
			mu.Unlock()
			qobs.Record(ctx, "create", nil, append([]any{"resource", ns + "/" + mr}, wall(r.xr)...)...)
			if !del {
				return
			}
			time.Sleep(delay)
			if err := kube.Delete(ctx, xr); err == nil {
				mu.Lock()
				r.deleted = true
				mu.Unlock()
				qobs.Record(ctx, "delete", nil, append([]any{"resource", ns + "/" + mr}, wall(r.xr)...)...)
			}
		}()
	}
	wg.Wait()
	quiet := func() bool {
		for _, r := range all {
			d := &v1alpha1.Database{}
			err := kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: r.mr}, d)
			if r.deleted && err == nil {
				return false // not gone yet
			}
			if !r.deleted && (err != nil || d.GetCondition("Ready").Status != "True") {
				return false
			}
		}
		return true
	}
	deadline := time.Now().Add(*wait)
	for !quiet() && time.Now().Before(deadline) {
		time.Sleep(time.Second)
	}
	fmt.Fprintf(os.Stderr, "driver: quiet=%v after waiting\n", quiet())
	for _, r := range all {
		crossdb.AuditOne(ctx, kube, ns, r.mr, r.conn, wall("audit")...)
	}
	qobs.Record(ctx, "settle", nil, wall("audit")...)
}

// composed waits for the Database an AppDatabase composes, and returns its name.
func composed(ctx context.Context, kube client.Client, xrName string) string {
	for {
		xr := &unstructured.Unstructured{}
		xr.SetGroupVersionKind(appDatabase)
		if err := kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: xrName}, xr); err == nil {
			refs, _, _ := unstructured.NestedSlice(xr.Object, "spec", "crossplane", "resourceRefs")
			for _, ref := range refs {
				if m, ok := ref.(map[string]any); ok && m["kind"] == "Database" && m["name"] != nil {
					return m["name"].(string)
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// clean deletes every AppDatabase and Database, removing finalizers a
// broken provider left behind.
func clean(ctx context.Context, kube client.Client) {
	xrs := &unstructured.UnstructuredList{}
	xrs.SetGroupVersionKind(appDatabase.GroupVersion().WithKind("AppDatabaseList"))
	if err := kube.List(ctx, xrs, client.InNamespace(ns)); err == nil {
		for i := range xrs.Items {
			_ = kube.Delete(ctx, &xrs.Items[i])
		}
	}
	for i := 0; i < 30; i++ {
		dbs := &v1alpha1.DatabaseList{}
		if err := kube.List(ctx, dbs, client.InNamespace(ns)); err != nil || len(dbs.Items) == 0 {
			return
		}
		for j := range dbs.Items {
			d := &dbs.Items[j]
			_ = kube.Delete(ctx, d)
			if meta.FinalizerExists(d, "finalizer.managedresource.crossplane.io") {
				meta.RemoveFinalizer(d, "finalizer.managedresource.crossplane.io")
				_ = kube.Update(ctx, d)
			}
		}
		time.Sleep(time.Second)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "driver:", err)
	os.Exit(1)
}
