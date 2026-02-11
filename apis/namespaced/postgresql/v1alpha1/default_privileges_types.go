package v1alpha1

import (
	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpv2 "github.com/crossplane/crossplane-runtime/v2/apis/common/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true

// A DefaultPrivileges represents the declarative state of a PostgreSQL DefaultPrivileges.
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
	xpv2.ManagedResourceSpec `json:",inline"`
	ForProvider              DefaultPrivilegesParameters `json:"forProvider"`
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

	// TargetRole is the role whose future objects will have default privileges applied.
	// When this role creates new objects, the specified privileges are automatically
	// granted. Maps to FOR ROLE in ALTER DEFAULT PRIVILEGES.
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

	// Role is the role that will receive the default privileges (the grantee).
	// Maps to TO in ALTER DEFAULT PRIVILEGES ... GRANT ... TO role.
	// +optional
	// +crossplane:generate:reference:type=Role
	Role *string `json:"role,omitempty"`

	// RoleRef to which default privileges are granted.
	// +immutable
	// +optional
	RoleRef *xpv1.NamespacedReference `json:"roleRef,omitempty"`

	// RoleSelector selects a reference to a Role this default grant is for.
	// +immutable
	// +optional
	RoleSelector *xpv1.NamespacedSelector `json:"roleSelector,omitempty"`

	// Database in which the default privileges are applied
	// +optional
	// +crossplane:generate:reference:type=Database
	Database *string `json:"database,omitempty"`

	// DatabaseRef references the database object this default grant it for.
	// +immutable
	// +optional
	DatabaseRef *xpv1.NamespacedReference `json:"databaseRef,omitempty"`

	// DatabaseSelector selects a reference to a Database this grant is for.
	// +immutable
	// +optional
	DatabaseSelector *xpv1.NamespacedSelector `json:"databaseSelector,omitempty"`

	// Schema in which the default privileges are applied
	// +required
	Schema *string `json:"schema,omitempty"`
}

// +kubebuilder:object:root=true

// DefaultPrivilegesList contains a list of DefaultPrivileges.
type DefaultPrivilegesList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DefaultPrivileges `json:"items"`
}
