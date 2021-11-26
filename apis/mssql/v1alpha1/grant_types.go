/*
Copyright 2021 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A GrantSpec defines the desired state of a Grant.
type GrantSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       GrantParameters `json:"forProvider"`
}

// GrantPermission represents a permission to be granted
// +kubebuilder:validation:Pattern:=^[A-Z_ ]+$
type GrantPermission string

// GrantPermissions is a list of the privileges to be granted
// If Permissions are specified, we should have at least one
// +kubebuilder:validation:MinItems:=1
type GrantPermissions []GrantPermission

// ToStringSlice converts the slice of privileges to strings
func (gp *GrantPermissions) ToStringSlice() []string {
	if gp == nil {
		return []string{}
	}
	out := make([]string, len(*gp))
	for i, v := range *gp {
		out[i] = string(v)
	}
	return out
}

// GrantParameters define the desired state of a MSSQL grant instance.
type GrantParameters struct {
	// Permissions to be granted.
	// See https://docs.microsoft.com/en-us/sql/t-sql/statements/grant-database-permissions-transact-sql?view=sql-server-ver15#remarks
	// for available privileges.
	Permissions GrantPermissions `json:"permissions"`

	// User this grant is for.
	// +optional
	// +crossplane:generate:reference:type=User
	User *string `json:"user,omitempty"`

	// UserRef references the user object this grant is for.
	// +immutable
	// +optional
	UserRef *xpv1.Reference `json:"userRef,omitempty"`

	// UserSelector selects a reference to a User this grant is for.
	// +immutable
	// +optional
	UserSelector *xpv1.Selector `json:"userSelector,omitempty"`

	// Database this grant is for.
	// +optional
	// +crossplane:generate:reference:type=Database
	Database *string `json:"database,omitempty"`

	// DatabaseRef references the database object this grant it for.
	// +immutable
	// +optional
	DatabaseRef *xpv1.Reference `json:"databaseRef,omitempty"`

	// DatabaseSelector selects a reference to a Database this grant is for.
	// +immutable
	// +optional
	DatabaseSelector *xpv1.Selector `json:"databaseSelector,omitempty"`
}

// A GrantStatus represents the observed state of a Grant.
type GrantStatus struct {
	xpv1.ResourceStatus `json:",inline"`
}

// +kubebuilder:object:root=true

// A Grant represents the declarative state of a MSSQL grant.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="ROLE",type="string",JSONPath=".spec.forProvider.user"
// +kubebuilder:printcolumn:name="DATABASE",type="string",JSONPath=".spec.forProvider.database"
// +kubebuilder:printcolumn:name="PERMISSIONS",type="string",JSONPath=".spec.forProvider.permissions"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,sql}
type Grant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GrantSpec   `json:"spec"`
	Status GrantStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GrantList contains a list of Grant
type GrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Grant `json:"items"`
}
