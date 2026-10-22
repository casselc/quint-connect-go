// Package crossdb runs the dbaas provider (package provider), with
// crossplane-runtime's own managed reconciler, in a qenv simulation:
//   - a Kubernetes API (controller-runtime fake client) whose requests can
//     fail, or be applied with their response lost;
//   - the dbaas cloud across a lossy network, throttling, provisioning
//     asynchronously and reading eventually consistently;
//   - the provider as a process that crashes and restarts;
//   - clients that create Databases and delete some of them.
//
// After a settle, an audit records each resource's phase and connection
// secret: the same steps the real environment records (kind/).
package crossdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/casselc/quint-connect-go/examples/crossdb/apis/v1alpha1"
	"github.com/casselc/quint-connect-go/examples/crossdb/dbaas"
	"github.com/casselc/quint-connect-go/examples/crossdb/provider"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// Scheme has the Kubernetes and dbaas types.
var Scheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	return s
}()

// Config of a simulated run.
type Config struct {
	Mutation provider.Mutation
	// Resources created (default 3); each is perhaps deleted later.
	Resources int
	NoCrashes bool
	// VisibilityLag of the cloud's reads (default 1s).
	VisibilityLag time.Duration
}

// Debug, if set, receives a line per reconcile.
var Debug func(string)

// ErrUnavailable is a Kubernetes API request that failed; for a write, it
// may or may not have been applied.
var ErrUnavailable = errors.New("kube: the API server did not answer")

// kubeAPI is the fake API server behind interceptors: every request is a
// seam, and may fail ("fault.kube.request" 1) or, for a write, be applied
// with its response lost (2).
var kubeRequest = qenv.Faults("kube.request", "unavailable", "lostResponse")

func kubeAPI(s *qenv.Sim, raw client.WithWatch) client.Client {
	write := func(ctx context.Context, apply func() error) error {
		qenv.Yield(ctx)
		f := s.FaultOf(kubeRequest)
		if f == "unavailable" {
			return ErrUnavailable
		}
		if err := apply(); err != nil {
			return err
		}
		if f == "lostResponse" {
			return ErrUnavailable
		}
		return nil
	}
	return interceptor.NewClient(raw, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			qenv.Yield(ctx)
			if s.FaultOf(kubeRequest.Only("unavailable")) != "" {
				return ErrUnavailable
			}
			err := cl.Get(ctx, key, obj, opts...)
			retime(obj, s.ToReal)
			return err
		},
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			qenv.Yield(ctx)
			err := cl.List(ctx, list, opts...)
			if l, ok := list.(*v1alpha1.DatabaseList); ok {
				for i := range l.Items {
					retime(&l.Items[i], s.ToReal)
				}
			}
			return err
		},
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			return write(ctx, func() error {
				retime(obj, s.ToVirtual)
				defer retime(obj, s.ToReal)
				return cl.Create(ctx, obj, opts...)
			})
		},
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			return write(ctx, func() error {
				retime(obj, s.ToVirtual)
				defer retime(obj, s.ToReal)
				return cl.Update(ctx, obj, opts...)
			})
		},
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
			return write(ctx, func() error {
				retime(obj, s.ToVirtual)
				defer retime(obj, s.ToReal)
				return cl.Patch(ctx, obj, p, opts...)
			})
		},
		SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			return write(ctx, func() error { return cl.SubResource(sub).Update(ctx, obj, opts...) })
		},
	})
}

// timeAnnotations are crossplane-runtime's timestamps, which it writes from
// the real clock and compares with it.
var timeAnnotations = []string{meta.AnnotationKeyExternalCreatePending, meta.AnnotationKeyExternalCreateSucceeded, meta.AnnotationKeyExternalCreateFailed}

// retime translates crossplane-runtime's timestamps at the API seam: they
// are stored in virtual time and handed to the reconciler in real time, so
// its grace periods measure virtual time.
func retime(obj client.Object, to func(time.Time) time.Time) {
	a := obj.GetAnnotations()
	for _, k := range timeAnnotations {
		if v, ok := a[k]; ok {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				a[k] = to(t).Format(time.RFC3339)
			}
		}
	}
}

// mgr is the part of a manager the managed reconciler uses.
type mgr struct {
	manager.Manager
	kube client.Client
}

func (m mgr) GetClient() client.Client   { return m.kube }
func (m mgr) GetScheme() *runtime.Scheme { return Scheme }

// NewDatabase is a Database resource as a client creates it.
func NewDatabase(ns, name string) *v1alpha1.Database {
	return &v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name)},
		Spec: v1alpha1.DatabaseSpec{
			ManagedResourceSpec: xpv2.ManagedResourceSpec{WriteConnectionSecretToReference: &xpv2.LocalSecretReference{Name: name + "-conn"}},
			ForProvider: v1alpha1.DatabaseParameters{Engine: "postgres",
				PasswordSecretRef: xpv2.LocalSecretKeySelector{LocalSecretReference: xpv2.LocalSecretReference{Name: name + "-admin"}, Key: "password"}},
		},
	}
}

// Run deploys the provider with c making the choices, runs the clients
// for 10 s of virtual time, settles for up to 60 s (longer than the
// creation grace period, which delays deleting a just-created database),
// then audits.
func Run(c qenv.Chooser, cfg Config) error {
	if cfg.Resources == 0 {
		cfg.Resources = 3
	}
	if cfg.VisibilityLag == 0 {
		cfg.VisibilityLag = time.Second
	}
	s := qenv.New(c)
	defer s.Close()
	raw := fake.NewClientBuilder().WithScheme(Scheme).WithStatusSubresource(&v1alpha1.Database{}).Build()
	kube := kubeAPI(s, raw)
	cloud := dbaas.NewCloud()
	cloud.VisibilityLag = cfg.VisibilityLag
	cloud.Record = func(ctx context.Context, action string, kv ...any) { qobs.Record(ctx, action, nil, kv...) }
	api := dbaas.NewSim(s, cloud)

	pw := 0
	s.Process("provider", !cfg.NoCrashes, func(ctx context.Context) {
		opts := provider.ReconcilerOptions(kube, api, provider.Options{Mutation: cfg.Mutation, NewPassword: func() string {
			pw++
			return fmt.Sprintf("pw%d", pw)
		}})
		r := managed.NewReconciler(mgr{kube: kube}, resource.ManagedKind(v1alpha1.DatabaseGVK), opts...)
		for { // reconcile every resource, every 500 ms
			list := &v1alpha1.DatabaseList{}
			if err := kube.List(ctx, list); err == nil {
				for _, d := range list.Items {
					res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&d)})
					if Debug != nil {
						after := &v1alpha1.Database{}
						_ = raw.Get(ctx, client.ObjectKeyFromObject(&d), after)
						an := after.GetAnnotations()
						Debug(fmt.Sprintf("%v reconcile %s: %v err=%v ext=%q pending=%s ok=%s failed=%s deleting=%v", s.Elapsed(), d.Name, res.Requeue, err,
							an["crossplane.io/external-name"], an[meta.AnnotationKeyExternalCreatePending], an[meta.AnnotationKeyExternalCreateSucceeded], an[meta.AnnotationKeyExternalCreateFailed], after.DeletionTimestamp != nil))
					}
				}
			}
			qenv.Sleep(ctx, 500*time.Millisecond)
		}
	})
	for i := 1; i <= cfg.Resources; i++ {
		name := fmt.Sprintf("db%d", i)
		s.Spawn("client."+name, func(ctx context.Context) {
			d := NewDatabase("default", name)
			if err := raw.Create(ctx, d); err != nil {
				panic(err)
			}
			qobs.Record(ctx, "create", nil, "resource", "default/"+name)
			// keep it, or delete it after 0.5 to 4 s
			if k := s.Choose("sched.client.delete", 9); k > 0 {
				qenv.Sleep(ctx, time.Duration(k)*500*time.Millisecond)
				if err := raw.Delete(ctx, d); err == nil {
					qobs.Record(ctx, "delete", nil, "resource", "default/"+name)
				}
			}
		})
	}
	if err := s.Run(10 * time.Second); err != nil {
		return err
	}
	if err := s.Settle(60 * time.Second); err != nil {
		return err
	}
	Audit(s.Context(), raw, cfg.Resources)
	qobs.Record(s.Context(), "settle", nil)
	return nil
}

// Audit records each resource's phase (live, deleting, gone) and its
// connection secret, as read from the API server.
func Audit(ctx context.Context, kube client.Client, resources int) {
	for i := 1; i <= resources; i++ {
		name := fmt.Sprintf("db%d", i)
		AuditOne(ctx, kube, "default", name, name+"-conn")
	}
}

// AuditOne records one resource's phase and connection secret, with extra
// step fields (the real environment adds its thread and clock).
func AuditOne(ctx context.Context, kube client.Client, ns, name, connSecret string, extra ...any) {
	d := &v1alpha1.Database{}
	err := kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, d)
	phase := "live"
	switch {
	case kerrors.IsNotFound(err):
		phase = "gone"
	case err != nil:
		phase = "unknown"
	case d.DeletionTimestamp != nil:
		phase = "deleting"
	}
	qobs.Record(ctx, "audit", nil, append([]any{"resource", ns + "/" + name, "phase", phase}, extra...)...)
	if phase != "live" {
		return
	}
	endpoint, fp := "", ""
	sec := &corev1.Secret{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: connSecret}, sec); err == nil {
		endpoint = string(sec.Data["endpoint"])
		if pw := string(sec.Data["password"]); pw != "" {
			fp = dbaas.Fingerprint(pw)
		}
	}
	qobs.Record(ctx, "secret", nil, append([]any{"resource", ns + "/" + name, "endpoint", endpoint, "password", fp}, extra...)...)
}
