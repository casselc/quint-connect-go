// Package rollout is a multi-cluster rollout controller: it moves one
// Deployment to a target image across a fleet of member clusters, at most
// MaxUnavailable clusters at a time. A cluster is in progress from the
// moment its spec has the target image until its status reports that
// generation fully rolled out.
//
// Each reconcile reads every member's Deployment from its API server,
// decides, and patches at most what the budget allows. It is a level-based
// controller: it keeps no state between reconciles, so a restart loses
// nothing (unless mutated with InMemoryProgress).
//
// Model steps (../model/fleet.qnt): observe, for each Deployment read (its
// resourceVersion orders the evidence), and patch, for each spec update
// the API server applied.
package rollout

import (
	"context"
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/casselc/quint-connect-go/qobs"
)

// Mutation injects a bug.
type Mutation int

const (
	Faithful Mutation = iota
	// IgnoreObservedGeneration counts a cluster as done when its status
	// shows all replicas updated and available, without checking that the
	// status is for the current generation. Right after a patch the status
	// still describes the old generation, fully rolled out, so the next
	// cluster is patched at once.
	IgnoreObservedGeneration
	// InMemoryProgress counts the clusters it patched and has not yet seen
	// finish, in memory, instead of reading status. A restart forgets them.
	InMemoryProgress
)

// Clusters is the controller's view of the fleet. Production implements
// it over multicluster-runtime (cmd/fleet-controller), with uncached reads;
// the simulation over controller-runtime fake clients (package kubesim).
type Clusters interface {
	Names() []string
	Reader(ctx context.Context, cluster string) (client.Reader, error)
	Writer(ctx context.Context, cluster string) (client.Writer, error)
}

// Controller reconciles the fleet.
type Controller struct {
	Clusters       Clusters
	Key            client.ObjectKey // the Deployment, in every member
	Image          string           // the target image
	MaxUnavailable int
	Mutation       Mutation
	// RecordPatches records a patch step when an update succeeds. Leave it
	// off when the API server records patches itself (kubesim does, at the
	// moment it applies them).
	RecordPatches bool

	seen     map[string]string // cluster -> resourceVersion last recorded
	inFlight map[string]bool   // InMemoryProgress
}

type member struct {
	name    string
	dep     *appsv1.Deployment
	updated bool // the spec has the target image
	done    bool // and the status says it is rolled out
}

// Done reports whether d's status describes its current generation, fully
// rolled out. (IgnoreObservedGeneration leaves out the generation check.)
func (c *Controller) complete(d *appsv1.Deployment) bool {
	replicas := int32(1)
	if d.Spec.Replicas != nil {
		replicas = *d.Spec.Replicas
	}
	s := d.Status
	rolled := s.UpdatedReplicas == replicas && s.AvailableReplicas == replicas && s.Replicas == replicas
	if c.Mutation == IgnoreObservedGeneration {
		return rolled
	}
	return s.ObservedGeneration >= d.Generation && rolled
}

// Image is a Deployment's (first container's) image.
func Image(d *appsv1.Deployment) string {
	if cs := d.Spec.Template.Spec.Containers; len(cs) > 0 {
		return cs[0].Image
	}
	return ""
}

// Observe records an observe step for a Deployment read from a cluster,
// unless this controller already recorded that resourceVersion.
func (c *Controller) Observe(ctx context.Context, cluster string, d *appsv1.Deployment) {
	if c.seen == nil {
		c.seen = map[string]string{}
	}
	if c.seen[cluster] == d.ResourceVersion {
		return
	}
	c.seen[cluster] = d.ResourceVersion
	replicas := int32(1)
	if d.Spec.Replicas != nil {
		replicas = *d.Spec.Replicas
	}
	qobs.Record(ctx, "observe", nil, "cluster", cluster, "rv", rv(d), "image", Image(d), "generation", d.Generation,
		"observedGeneration", d.Status.ObservedGeneration, "updated", d.Status.UpdatedReplicas,
		"available", d.Status.AvailableReplicas, "replicas", replicas, "current", d.Status.Replicas)
}

// RecordPatch records a patch step for an update the API server applied.
func RecordPatch(ctx context.Context, cluster string, d *appsv1.Deployment) {
	qobs.Record(ctx, "patch", nil, "cluster", cluster, "image", Image(d), "rv", rv(d), "generation", d.Generation)
}

func rv(d *appsv1.Deployment) int64 {
	n, _ := strconv.ParseInt(d.ResourceVersion, 10, 64)
	return n
}

// Reconcile reads the fleet and patches what the budget allows. It
// returns whether the rollout is complete.
func (c *Controller) Reconcile(ctx context.Context) (bool, error) {
	var ms []member
	for _, name := range c.Clusters.Names() {
		r, err := c.Clusters.Reader(ctx, name)
		if err != nil {
			return false, err
		}
		d := &appsv1.Deployment{}
		if err := r.Get(ctx, c.Key, d); err != nil {
			return false, fmt.Errorf("%s: %w", name, err)
		}
		c.Observe(ctx, name, d)
		m := member{name: name, dep: d, updated: Image(d) == c.Image}
		m.done = m.updated && c.complete(d)
		ms = append(ms, m)
	}
	inProgress := 0
	for _, m := range ms {
		if m.updated && !m.done {
			inProgress++
		}
	}
	if c.Mutation == InMemoryProgress {
		if c.inFlight == nil {
			c.inFlight = map[string]bool{}
		}
		for _, m := range ms {
			if m.done {
				delete(c.inFlight, m.name)
			}
		}
		inProgress = len(c.inFlight)
	}
	all := true
	for _, m := range ms {
		all = all && m.done
		if m.updated || inProgress >= c.MaxUnavailable {
			continue
		}
		w, err := c.Clusters.Writer(ctx, m.name)
		if err != nil {
			return false, err
		}
		d := m.dep.DeepCopy()
		d.Spec.Template.Spec.Containers[0].Image = c.Image
		if err := w.Update(ctx, d); err != nil {
			return false, fmt.Errorf("%s: %w", m.name, err) // a conflict: the next reconcile reads again
		}
		if c.RecordPatches {
			RecordPatch(ctx, m.name, d)
		}
		if c.Mutation == InMemoryProgress {
			c.inFlight[m.name] = true
		}
		inProgress++
	}
	return all, nil
}
