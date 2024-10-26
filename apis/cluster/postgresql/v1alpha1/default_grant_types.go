package v1alpha1

import (
	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true

// A Grant represents the declarative state of a PostgreSQL grant.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="ROLE",type="string",JSONPath=".spec.forProvider.role"
// +kubebuilder:printcolumn:name="MEMBER OF",type="string",JSONPath=".spec.forProvider.memberOf"
// +kubebuilder:printcolumn:name="DATABASE",type="string",JSONPath=".spec.forProvider.database"
// +kubebuilder:printcolumn:name="PRIVILEGES",type="string",JSONPath=".spec.forProvider.privileges"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,sql}
type DefaultGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DefaultGrantSpec   `json:"spec"`
	Status DefaultGrantStatus `json:"status,omitempty"`
}

// A DefaultGrantSpec defines the desired state of a Default Grant.
type DefaultGrantSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       DefaultGrantParameters `json:"forProvider"`
}

// A DefaultGrantStatus represents the observed state of a Grant.
type DefaultGrantStatus struct {
	xpv1.ResourceStatus `json:",inline"`
}

// DefaultGrantParameters defines the desired state of a Default Grant.
type DefaultGrantParameters struct {
	// Privileges to be granted.
	// See https://www.postgresql.org/docs/current/sql-grant.html for available privileges.
	// +optional
	Privileges GrantPrivileges `json:"privileges,omitempty"`

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

// DefaultGrantList contains a list of DefaultGrant.
type DefaultGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DefaultGrant `json:"items"`
}
