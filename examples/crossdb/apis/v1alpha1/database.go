// Package v1alpha1 is the provider's API: the Database managed resource,
// a namespaced Crossplane v2 managed resource.
//
// +kubebuilder:object:generate=true
// +groupName=dbaas.example.org
package v1alpha1

import (
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// Group, version and kind.
var (
	GroupVersion  = schema.GroupVersion{Group: "dbaas.example.org", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
	DatabaseGVK   = GroupVersion.WithKind("Database")
)

func init() { SchemeBuilder.Register(&Database{}, &DatabaseList{}) }

// DatabaseParameters are the desired state of a database.
type DatabaseParameters struct {
	// Engine is the database engine, e.g. postgres.
	Engine string `json:"engine"`
	// PasswordSecretRef is the Secret (in the resource's namespace) holding
	// the admin password. The provider generates it if it does not exist,
	// before creating the database.
	PasswordSecretRef xpv2.LocalSecretKeySelector `json:"passwordSecretRef"`
}

// DatabaseObservation is the observed state of a database.
type DatabaseObservation struct {
	ID                  string `json:"id,omitempty"`
	Status              string `json:"status,omitempty"`
	Endpoint            string `json:"endpoint,omitempty"`
	PasswordFingerprint string `json:"passwordFingerprint,omitempty"`
}

// DatabaseSpec is the spec of a Database.
type DatabaseSpec struct {
	xpv2.ManagedResourceSpec `json:",inline"`
	ForProvider              DatabaseParameters `json:"forProvider"`
}

// DatabaseStatus is the status of a Database.
type DatabaseStatus struct {
	xpv2.ManagedResourceStatus `json:",inline"`
	AtProvider                 DatabaseObservation `json:"atProvider,omitempty"`
}

// A Database is a database in the dbaas cloud.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,managed,dbaas}
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="ID",type="string",JSONPath=".status.atProvider.id"
type Database struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DatabaseSpec   `json:"spec"`
	Status DatabaseStatus `json:"status,omitempty"`
}

// DatabaseList is a list of Databases.
//
// +kubebuilder:object:root=true
type DatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Database `json:"items"`
}

// The methods below make Database a resource.ModernManaged (what
// crossplane's angryjet generates for real providers).

// GetCondition of this Database.
func (d *Database) GetCondition(ct xpv2.ConditionType) xpv2.Condition {
	return d.Status.GetCondition(ct)
}

// SetConditions of this Database.
func (d *Database) SetConditions(c ...xpv2.Condition) { d.Status.SetConditions(c...) }

// GetManagementPolicies of this Database.
func (d *Database) GetManagementPolicies() xpv2.ManagementPolicies {
	return d.Spec.ManagementPolicies
}

// SetManagementPolicies of this Database.
func (d *Database) SetManagementPolicies(p xpv2.ManagementPolicies) {
	d.Spec.ManagementPolicies = p
}

// GetProviderConfigReference of this Database.
func (d *Database) GetProviderConfigReference() *xpv2.ProviderConfigReference {
	return d.Spec.ProviderConfigReference
}

// SetProviderConfigReference of this Database.
func (d *Database) SetProviderConfigReference(r *xpv2.ProviderConfigReference) {
	d.Spec.ProviderConfigReference = r
}

// GetWriteConnectionSecretToReference of this Database.
func (d *Database) GetWriteConnectionSecretToReference() *xpv2.LocalSecretReference {
	return d.Spec.WriteConnectionSecretToReference
}

// SetWriteConnectionSecretToReference of this Database.
func (d *Database) SetWriteConnectionSecretToReference(r *xpv2.LocalSecretReference) {
	d.Spec.WriteConnectionSecretToReference = r
}
