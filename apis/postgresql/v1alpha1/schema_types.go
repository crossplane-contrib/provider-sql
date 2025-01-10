/*
Copyright 2024 The Crossplane Authors.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// A SchemaSpec defines the desired state of a Schema.
type SchemaSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       SchemaParameters `json:"forProvider"`
}

// SchemaParameters define the desired state of a PostgreSQL schema.
type SchemaParameters struct {
	// Role for ownership of this schema.
	// +optional
	// +crossplane:generate:reference:type=Role
	Role *string `json:"role,omitempty"`

	// RoleRef references the role object this schema is for.
	// +immutable
	// +optional
	RoleRef *xpv1.Reference `json:"roleRef,omitempty"`

	// RoleSelector selects a reference to a Role this schema is for.
	// +immutable
	// +optional
	RoleSelector *xpv1.Selector `json:"roleSelector,omitempty"`

	// Database this schema is for.
	// +optional
	// +crossplane:generate:reference:type=Database
	Database *string `json:"database,omitempty"`

	// DatabaseRef references the database object this schema is for.
	// +immutable
	// +optional
	DatabaseRef *xpv1.Reference `json:"databaseRef,omitempty"`

	// DatabaseSelector selects a reference to a Database this schema is for.
	// +immutable
	// +optional
	DatabaseSelector *xpv1.Selector `json:"databaseSelector,omitempty"`

	// RevokePublicOnSchema apply a "REVOKE ALL ON SCHEMA public FROM public" statement
	// +optional
	RevokePublicOnSchema *bool `json:"revokePublicOnSchema,omitempty" default:"false"`
}

// A SchemaStatus represents the observed state of a Schema.
type SchemaStatus struct {
	xpv1.ResourceStatus `json:",inline"`
}

// +kubebuilder:object:root=true

// A Schema represents the declarative state of a PostgreSQL schema.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="ROLE",type="string",JSONPath=".spec.forProvider.role"
// +kubebuilder:printcolumn:name="DATABASE",type="string",JSONPath=".spec.forProvider.database"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,sql}
type Schema struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SchemaSpec   `json:"spec"`
	Status SchemaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SchemaList contains a list of Schema
type SchemaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Schema `json:"items"`
}
