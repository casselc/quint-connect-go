// Package provider is a Crossplane provider for dbaas databases, built on
// crossplane-runtime's managed reconciler. The reconciler is Crossplane's
// own (create-pending annotations, finalizers, connection publishing);
// this package supplies the external client and how the reconciler is
// configured.
//
// The faithful design:
//   - The external name is deterministic: a client token derived from the
//     resource's UID, set before anything is created. Create passes it to
//     the cloud, which makes Create idempotent, and Observe finds the
//     database by it. So the reconciler may safely retry a create whose
//     outcome it does not know (WithDeterministicExternalName), whether a
//     response was lost or the provider crashed.
//   - The admin password lives in a Secret (spec.forProvider.
//     passwordSecretRef) written before the database is created. Every
//     Observe reads it, publishes it with the endpoint, and compares its
//     fingerprint with the cloud's; Update resets a password that differs.
package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/casselc/quint-connect-go/examples/crossdb/apis/v1alpha1"
	"github.com/casselc/quint-connect-go/examples/crossdb/dbaas"
)

// Mutation injects a bug.
type Mutation int

const (
	Faithful Mutation = iota
	// GeneratedExternalName uses the cloud's generated ID as the external
	// name, set by Create (a common provider pattern), still with a client
	// token. A crash after the create, before the reconciler saves the
	// name, leaves the create incomplete: Crossplane refuses to go on, the
	// resource is never ready, and deleting it leaks the database.
	GeneratedExternalName
	// NoClientToken is GeneratedExternalName without the client token: a
	// create whose response is lost is retried, and makes a second
	// database.
	NoClientToken
	// PasswordFromCreate generates the password in Create and publishes it
	// only as the creation's connection details. A lost response (the
	// retry generates another password, which the idempotent cloud
	// ignores) or a crash before publishing leaves a secret that does not
	// open the database.
	PasswordFromCreate
)

// Options configure the provider.
type Options struct {
	Mutation Mutation
	// NewPassword generates a password (default: crypto/rand). Simulations
	// pass a deterministic one.
	NewPassword func() string
	// PollInterval of the managed reconciler (default 1 minute).
	PollInterval time.Duration
}

// Token is the deterministic external name of a resource: its client token.
func Token(mg resource.Managed) string { return "xp-" + string(mg.GetUID()) }

// Owner is how the cloud tags a resource's database: namespace/name.
func Owner(mg resource.Managed) string { return mg.GetNamespace() + "/" + mg.GetName() }

type connector struct {
	kube client.Client
	api  dbaas.API
	o    Options
}

// Connector returns the managed reconciler's external connector.
func Connector(kube client.Client, api dbaas.API, o Options) managed.ExternalConnector {
	if o.NewPassword == nil {
		o.NewPassword = func() string {
			b := make([]byte, 12)
			_, _ = rand.Read(b)
			return hex.EncodeToString(b)
		}
	}
	return &connector{kube: kube, api: api, o: o}
}

func (c *connector) Connect(_ context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	if _, ok := mg.(*v1alpha1.Database); !ok {
		return nil, errors.New("not a Database")
	}
	return &external{connector: c}, nil
}

// ReconcilerOptions configure the managed reconciler for the provider:
// faithfully, with a deterministic external name set by an initializer.
func ReconcilerOptions(kube client.Client, api dbaas.API, o Options) []managed.ReconcilerOption {
	opts := []managed.ReconcilerOption{
		managed.WithExternalConnector(Connector(kube, api, o)),
		// Wait this long for a just-created database to become visible
		// before believing it does not exist: the cloud's reads are
		// eventually consistent, and a resource deleted right after its
		// create would otherwise lose its finalizer and leak the database.
		managed.WithCreationGracePeriod(30 * time.Second),
	}
	if o.PollInterval > 0 {
		opts = append(opts, managed.WithPollInterval(o.PollInterval))
	}
	if o.Mutation == GeneratedExternalName || o.Mutation == NoClientToken {
		// no initializer: the external name is whatever Create returns
		return append(opts, managed.WithInitializers())
	}
	return append(opts,
		managed.WithInitializers(managed.InitializerFn(func(ctx context.Context, mg resource.Managed) error {
			if meta.GetExternalName(mg) != "" {
				return nil
			}
			meta.SetExternalName(mg, Token(mg))
			return kube.Update(ctx, mg)
		})),
		managed.WithDeterministicExternalName(true))
}

type external struct{ *connector }

func (e *external) generated() bool {
	return e.o.Mutation == GeneratedExternalName || e.o.Mutation == NoClientToken
}

// find returns the resource's database, if it exists.
func (e *external) find(ctx context.Context, db *v1alpha1.Database) (dbaas.Database, bool, error) {
	var d dbaas.Database
	var err error
	if e.generated() {
		id := meta.GetExternalName(db)
		if id == "" {
			return d, false, nil
		}
		d, err = e.api.Get(ctx, id)
	} else {
		d, err = e.api.FindByToken(ctx, meta.GetExternalName(db))
	}
	if errors.Is(err, dbaas.ErrNotFound) {
		return d, false, nil
	}
	return d, err == nil, err
}

// password reads the admin password Secret ("" if it does not exist).
func (e *external) password(ctx context.Context, db *v1alpha1.Database) (string, error) {
	ref := db.Spec.ForProvider.PasswordSecretRef
	s := &corev1.Secret{}
	err := e.kube.Get(ctx, client.ObjectKey{Namespace: db.Namespace, Name: ref.Name}, s)
	if kerrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(s.Data[ref.Key]), nil
}

// ensurePassword returns the admin password, creating its Secret first if
// there is none.
func (e *external) ensurePassword(ctx context.Context, db *v1alpha1.Database) (string, error) {
	if pw, err := e.password(ctx, db); err != nil || pw != "" {
		return pw, err
	}
	ref := db.Spec.ForProvider.PasswordSecretRef
	pw := e.o.NewPassword()
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: db.Namespace, Name: ref.Name}, Data: map[string][]byte{ref.Key: []byte(pw)}}
	if err := e.kube.Create(ctx, s); err != nil {
		if kerrors.IsAlreadyExists(err) {
			return e.password(ctx, db) // created by an earlier attempt
		}
		return "", err
	}
	return pw, nil
}

func (e *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	db := mg.(*v1alpha1.Database)
	d, ok, err := e.find(ctx, db)
	if err != nil || !ok {
		return managed.ExternalObservation{}, err
	}
	db.Status.AtProvider = v1alpha1.DatabaseObservation{ID: d.ID, Status: d.Status, Endpoint: d.Endpoint, PasswordFingerprint: d.PasswordFingerprint}
	switch d.Status {
	case "available":
		db.SetConditions(xpv2.Available())
	case "deleting":
		db.SetConditions(xpv2.Deleting())
	default:
		db.SetConditions(xpv2.Creating())
	}
	details := managed.ConnectionDetails{"endpoint": []byte(d.Endpoint), "username": []byte(d.Username)}
	upToDate := true
	if e.o.Mutation != PasswordFromCreate {
		pw, err := e.password(ctx, db)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		details["password"] = []byte(pw)
		upToDate = pw != "" && dbaas.Fingerprint(pw) == d.PasswordFingerprint
	}
	return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: upToDate || d.Status == "deleting", ConnectionDetails: details}, nil
}

func (e *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	db := mg.(*v1alpha1.Database)
	var pw string
	if e.o.Mutation == PasswordFromCreate {
		pw = e.o.NewPassword()
	} else {
		var err error
		if pw, err = e.ensurePassword(ctx, db); err != nil {
			return managed.ExternalCreation{}, fmt.Errorf("password secret: %w", err)
		}
	}
	in := dbaas.CreateInput{Name: db.Name, Owner: Owner(db), Engine: db.Spec.ForProvider.Engine, Password: pw}
	switch e.o.Mutation {
	case NoClientToken:
	case GeneratedExternalName:
		in.ClientToken = Token(db)
	default:
		in.ClientToken = meta.GetExternalName(db)
	}
	d, err := e.api.Create(ctx, in)
	if err != nil {
		return managed.ExternalCreation{}, err
	}
	if e.generated() {
		meta.SetExternalName(db, d.ID)
	}
	out := managed.ExternalCreation{}
	if e.o.Mutation == PasswordFromCreate {
		out.ConnectionDetails = managed.ConnectionDetails{"password": []byte(pw)}
	}
	return out, nil
}

func (e *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	db := mg.(*v1alpha1.Database)
	pw, err := e.password(ctx, db)
	if err != nil || pw == "" {
		return managed.ExternalUpdate{}, err
	}
	return managed.ExternalUpdate{}, e.api.SetPassword(ctx, db.Status.AtProvider.ID, pw)
}

func (e *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	db := mg.(*v1alpha1.Database)
	d, ok, err := e.find(ctx, db)
	if err != nil || !ok {
		return managed.ExternalDelete{}, err
	}
	err = e.api.Delete(ctx, d.ID)
	if errors.Is(err, dbaas.ErrNotFound) {
		err = nil
	}
	return managed.ExternalDelete{}, err
}

func (e *external) Disconnect(context.Context) error { return nil }
