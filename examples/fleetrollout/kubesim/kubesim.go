// Package kubesim simulates member clusters for the rollout controller in
// a qenv simulation. Each cluster is a controller-runtime fake client
// behind interceptors (the seam where Kubernetes' contract is enforced and
// its faults are injected), plus a Deployment controller that moves the
// status along.
//
// The contract, as the real API server and controller manager give it:
//   - a spec change bumps metadata.generation; the status catches up later
//     (status.observedGeneration lags, by a schedule choice);
//   - every write bumps resourceVersion, and an update with a stale one
//     fails with a conflict;
//   - a request can fail ("fault.kube.request" 1), or, for an update, be
//     applied with its response lost (2): the caller cannot tell.
//
// Patches are recorded as model steps here, where they take effect; so are
// other writers' updates (AsWriter, Replay), as scale steps.
package kubesim

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/casselc/quint-connect-go/examples/fleetrollout/rollout"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/sktrace"
)

// ErrUnavailable is a request that failed in the network or the API
// server; for a write, it may or may not have been applied.
var ErrUnavailable = errors.New("kubesim: the API server did not answer")

// kubeRequest is a request that failed, or, for an update, was applied
// with its response lost: the caller cannot tell them apart.
var kubeRequest = qenv.DeclareFaults("kube.request", "unavailable", "lostResponse")

// Cluster is a simulated member cluster.
type Cluster struct {
	Name string
	// Client is what controllers use: intercepted.
	Client client.Client
	raw    client.WithWatch
	sim    *qenv.Sim
	// PodTimes, if set, are the options of the time a replaced pod takes
	// to become ready ("sched.kube.pod"; the first, nominal, the shortest):
	// a production trace's (sktrace.Quantiles of its Lifetimes, scaled).
	// Unset: 100 to 300 ms.
	PodTimes []time.Duration
}

var scheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	return s
}()

// Web is the Deployment every member runs: default/web, 3 replicas.
var Web = client.ObjectKey{Namespace: "default", Name: "web"}

// NewDeployment is Web at image, rolled out.
func NewDeployment(image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: Web.Namespace, Name: Web.Name, Generation: 1},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](3),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: image}}},
			},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 3, UpdatedReplicas: 3, AvailableReplicas: 3, ReadyReplicas: 3},
	}
}

// NewCluster creates a cluster running Web at image, and its Deployment
// controller as a (never crashing) process.
func NewCluster(s *qenv.Sim, name, image string) *Cluster {
	raw := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&appsv1.Deployment{}).
		WithObjects(NewDeployment(image)).Build()
	c := &Cluster{Name: name, raw: raw, sim: s}
	c.Client = interceptor.NewClient(raw, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			qenv.Yield(ctx)
			if s.FaultOf(kubeRequest.Only("unavailable")) != "" {
				return ErrUnavailable
			}
			return cl.Get(ctx, key, obj, opts...)
		},
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			qenv.Yield(ctx)
			f := s.FaultOf(kubeRequest)
			if f == "unavailable" {
				return ErrUnavailable
			}
			if err := c.update(ctx, obj, opts...); err != nil {
				return err
			}
			if f == "lostResponse" {
				return ErrUnavailable // applied; the response is lost
			}
			return nil
		},
	})
	s.Process(name+".kcm", false, c.deploymentController)
	return c
}

// update applies a spec update as the API server does: generation bumps
// when the spec changes; the status is not touched.
func (c *Cluster) update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	d, ok := obj.(*appsv1.Deployment)
	if !ok {
		return c.raw.Update(ctx, obj, opts...)
	}
	cur := &appsv1.Deployment{}
	if err := c.raw.Get(ctx, client.ObjectKeyFromObject(d), cur); err != nil {
		return err
	}
	if d.ResourceVersion != cur.ResourceVersion {
		return fmt.Errorf("kubesim: conflict: resourceVersion %s, current %s", d.ResourceVersion, cur.ResourceVersion)
	}
	d.Generation = cur.Generation
	if !equality.Semantic.DeepEqual(d.Spec, cur.Spec) {
		d.Generation++
	}
	d.Status = cur.Status
	if err := c.raw.Update(ctx, d, opts...); err != nil {
		return err
	}
	if w, ok := ctx.Value(writerKey{}).(string); ok {
		qobs.Record(ctx, "scale", nil, "cluster", c.Name, "rv", rvOf(d), "generation", d.Generation, "writer", w)
		return nil
	}
	rollout.RecordPatch(ctx, c.Name, d)
	return nil
}

type writerKey struct{}

// AsWriter marks ctx's writes as another writer's (an autoscaler, a
// person with kubectl), not the rollout's: they are recorded as scale
// steps, the environment's.
func AsWriter(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, writerKey{}, name)
}

func rvOf(d *appsv1.Deployment) int64 {
	n, _ := strconv.ParseInt(d.ResourceVersion, 10, 64)
	return n
}

// Replay plays a SimKube trace's Deployment scalings onto the fleet's web
// Deployments, as another writer, while the rollout runs: each traced
// Deployment is mapped to a member (in the order they first appear), and
// each applied version sets its member's replicas (at most 6), at the
// trace's time divided by speed. Writes go through the members' clients,
// with their faults, and retry a conflict or a failure as a controller
// would (read, modify, update).
func (f *Fleet) Replay(s *qenv.Sim, tr *sktrace.Trace, speed float64) {
	members := map[string]*Cluster{}
	s.Process("workload", false, func(ctx context.Context) {
		ctx = AsWriter(ctx, "workload")
		start := qenv.Now(ctx)
		for _, e := range tr.Events {
			at := start.Add(time.Duration(float64(tr.Offset(e)) / speed))
			if d := at.Sub(qenv.Now(ctx)); d > 0 {
				qenv.Sleep(ctx, d)
			}
			for _, o := range e.Applied {
				if o.Kind() != "Deployment" {
					continue
				}
				m, ok := members[o.Key()]
				if !ok {
					m = f.Members[len(members)%len(f.Members)]
					members[o.Key()] = m
				}
				m.scale(ctx, int32(min(max(sktrace.Replicas(o), 1), 6)))
			}
		}
	})
}

// scale sets the web Deployment's replicas as another writer would.
func (c *Cluster) scale(ctx context.Context, replicas int32) {
	for range 5 {
		d := &appsv1.Deployment{}
		if err := c.Client.Get(ctx, Web, d); err != nil {
			continue
		}
		if *d.Spec.Replicas == replicas {
			return
		}
		d.Spec.Replicas = ptr.To(replicas)
		if err := c.Client.Update(ctx, d); err == nil {
			return
		}
		qenv.Sleep(ctx, 100*time.Millisecond)
	}
}

// deploymentController moves the status toward the spec: it observes a new
// generation after a lag, then replaces the pods one at a time (one
// unavailable during the rollout).
func (c *Cluster) deploymentController(ctx context.Context) {
	for {
		qenv.Sleep(ctx, 100*time.Millisecond)
		d := &appsv1.Deployment{}
		if err := c.raw.Get(ctx, Web, d); err != nil || d.Status.ObservedGeneration >= d.Generation {
			continue
		}
		// status lag: the controller manager is busy, or its watch is behind
		qenv.Sleep(ctx, time.Duration(c.sim.Choose("sched.kube.lag", 6))*100*time.Millisecond)
		gen := d.Generation
		replicas := *d.Spec.Replicas
		for updated := int32(0); updated <= replicas; updated++ {
			if err := c.raw.Get(ctx, Web, d); err != nil || d.Generation != gen {
				break // a newer spec: start again
			}
			d.Status.ObservedGeneration = gen
			d.Status.Replicas = replicas
			d.Status.UpdatedReplicas = updated
			d.Status.AvailableReplicas = replicas
			if updated < replicas {
				d.Status.AvailableReplicas = replicas - 1
			}
			d.Status.ReadyReplicas = d.Status.AvailableReplicas
			if err := c.raw.Status().Update(ctx, d); err != nil {
				break
			}
			if len(c.PodTimes) > 0 {
				qenv.Sleep(ctx, c.PodTimes[c.sim.Choose("sched.kube.pod", len(c.PodTimes))])
			} else {
				qenv.Sleep(ctx, time.Duration(1+c.sim.Choose("sched.kube.pod", 3))*100*time.Millisecond)
			}
		}
	}
}

// Fleet is a set of simulated clusters, as rollout.Clusters.
type Fleet struct{ Members []*Cluster }

// Names implements rollout.Clusters.
func (f *Fleet) Names() []string {
	var out []string
	for _, c := range f.Members {
		out = append(out, c.Name)
	}
	return out
}

func (f *Fleet) get(name string) (*Cluster, error) {
	for _, c := range f.Members {
		if c.Name == name {
			return c, nil
		}
	}
	return nil, fmt.Errorf("kubesim: no cluster %q", name)
}

// Reader implements rollout.Clusters.
func (f *Fleet) Reader(_ context.Context, name string) (client.Reader, error) {
	c, err := f.get(name)
	if err != nil {
		return nil, err
	}
	return c.Client, nil
}

// Writer implements rollout.Clusters.
func (f *Fleet) Writer(_ context.Context, name string) (client.Writer, error) {
	c, err := f.get(name)
	if err != nil {
		return nil, err
	}
	return c.Client, nil
}

// Read is the cluster's Deployment as stored, outside the simulation's
// faults.
func (c *Cluster) Read() *appsv1.Deployment {
	d := &appsv1.Deployment{}
	_ = c.raw.Get(context.Background(), Web, d)
	return d
}
