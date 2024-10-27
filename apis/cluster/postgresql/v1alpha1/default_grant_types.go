package v1alpha1

import (
	"context"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/reference"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:object:root=true

// A Grant represents the declarative state of a PostgreSQL grant.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="ROLE",type="string",JSONPath=".spec.forProvider.role"
// +kubebuilder:printcolumn:name="TARGET_ROLE",type="string",JSONPath=".spec.forProvider.targetRole"
// +kubebuilder:printcolumn:name="SCHEMA",type="string",JSONPath=".spec.forProvider.schema"
// +kubebuilder:printcolumn:name="DATABASE",type="string",JSONPath=".spec.forProvider.database"
// +kubebuilder:printcolumn:name="PRIVILEGES",type="string",JSONPath=".spec.forProvider.privileges"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,sql}
type DefaultPrivileges struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DefaultPrivilegesSpec   `json:"spec"`
	Status DefaultPrivilegesStatus `json:"status,omitempty"`
}

// A DefaultPrivilegesSpec defines the desired state of a Default Grant.
type DefaultPrivilegesSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       DefaultPrivilegesParameters `json:"forProvider"`
}

// A DefaultPrivilegesStatus represents the observed state of a Grant.
type DefaultPrivilegesStatus struct {
	xpv1.ResourceStatus `json:",inline"`
}

// DefaultPrivilegesParameters defines the desired state of a Default Grant.
type DefaultPrivilegesParameters struct {
	// Privileges to be granted.
	// See https://www.postgresql.org/docs/current/sql-grant.html for available privileges.
	// +optional
	Privileges GrantPrivileges `json:"privileges,omitempty"`

	// TargetRole is the role who owns objects on which the default privileges are granted.
	// See https://www.postgresql.org/docs/current/sql-alterdefaultprivileges.html
	// +required
	TargetRole *string `json:"targetRole"`

	// ObjectType to which the privileges are granted.
	// +kubebuilder:validation:Enum=table;sequence;function;schema;type
	// +required
	ObjectType *string `json:"objectType,omitempty"`

	// WithOption allows an option to be set on the grant.
	// See https://www.postgresql.org/docs/current/sql-grant.html for available
	// options for each grant type, and the effects of applying the option.
	// +kubebuilder:validation:Enum=ADMIN;GRANT
	// +optional
	WithOption *GrantOption `json:"withOption,omitempty"`

	// Role to which default privileges are granted
	// +optional
	Role *string `json:"role,omitempty"`

	// RoleRef to which default privileges are granted.
	// +immutable
	// +optional
	RoleRef *xpv1.Reference `json:"roleRef,omitempty"`

	// RoleSelector selects a reference to a Role this default grant is for.
	// +immutable
	// +optional
	RoleSelector *xpv1.Selector `json:"roleSelector,omitempty"`

	// Database in which the default privileges are applied
	// +optional
	Database *string `json:"database,omitempty"`

	// DatabaseRef references the database object this default grant it for.
	// +immutable
	// +optional
	DatabaseRef *xpv1.Reference `json:"databaseRef,omitempty"`

	// DatabaseSelector selects a reference to a Database this grant is for.
	// +immutable
	// +optional
	DatabaseSelector *xpv1.Selector `json:"databaseSelector,omitempty"`

	// Schema in which the default privileges are applied
	// +optional
	Schema *string `json:"schema,omitempty"`

	// SchemaRef references the database object this default grant it for.
	// +immutable
	// +optional
	SchemaRef *xpv1.Reference `json:"schemaRef,omitempty"`

	// SchemaSelector selects a reference to a Database this grant is for.
	// +immutable
	// +optional
	SchemaSelector *xpv1.Selector `json:"schemaSelector,omitempty"`
}

// +kubebuilder:object:root=true

// DefaultPrivilegesList contains a list of DefaultPrivileges.
type DefaultPrivilegesList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DefaultPrivileges `json:"items"`
}

// ResolveReferences of this DefaultPrivileges.
func (mg *DefaultPrivileges) ResolveReferences(ctx context.Context, c client.Reader) error {
	r := reference.NewAPIResolver(c, mg)

	// Resolve spec.forProvider.database
	rsp, err := r.Resolve(ctx, reference.ResolutionRequest{
		CurrentValue: reference.FromPtrValue(mg.Spec.ForProvider.Database),
		Reference:    mg.Spec.ForProvider.DatabaseRef,
		Selector:     mg.Spec.ForProvider.DatabaseSelector,
		To:           reference.To{Managed: &Database{}, List: &DatabaseList{}},
		Extract:      reference.ExternalName(),
	})
	if err != nil {
		return errors.Wrap(err, "spec.forProvider.database")
	}
	mg.Spec.ForProvider.Database = reference.ToPtrValue(rsp.ResolvedValue)
	mg.Spec.ForProvider.DatabaseRef = rsp.ResolvedReference

	// Resolve spec.forProvider.role
	rsp, err = r.Resolve(ctx, reference.ResolutionRequest{
		CurrentValue: reference.FromPtrValue(mg.Spec.ForProvider.Role),
		Reference:    mg.Spec.ForProvider.RoleRef,
		Selector:     mg.Spec.ForProvider.RoleSelector,
		To:           reference.To{Managed: &Role{}, List: &RoleList{}},
		Extract:      reference.ExternalName(),
	})
	if err != nil {
		return errors.Wrap(err, "spec.forProvider.role")
	}
	mg.Spec.ForProvider.Role = reference.ToPtrValue(rsp.ResolvedValue)
	mg.Spec.ForProvider.RoleRef = rsp.ResolvedReference

	// Resolve spec.forProvider.schema
	rsp, err = r.Resolve(ctx, reference.ResolutionRequest{
		CurrentValue: reference.FromPtrValue(mg.Spec.ForProvider.Schema),
		Reference:    mg.Spec.ForProvider.SchemaRef,
		Selector:     mg.Spec.ForProvider.SchemaSelector,
		To:           reference.To{Managed: &Role{}, List: &RoleList{}},
		Extract:      reference.ExternalName(),
	})
	if err != nil {
		return errors.Wrap(err, "spec.forProvider.schema")
	}
	mg.Spec.ForProvider.Schema = reference.ToPtrValue(rsp.ResolvedValue)
	mg.Spec.ForProvider.SchemaRef = rsp.ResolvedReference

	return nil
}
