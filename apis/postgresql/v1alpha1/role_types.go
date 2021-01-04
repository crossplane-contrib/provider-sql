/*
Copyright 2020 The Crossplane Authors.

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

// A RoleSpec defines the desired state of a Role.
type RoleSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       RoleParameters `json:"forProvider"`
}

// A RoleStatus represents the observed state of a Role.
type RoleStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          RoleObservation `json:"atProvider,omitempty"`
}

// RolePrivilege is the PostgreSQL identifier to add or remove a permission
// on a role.
// See https://www.postgresql.org/docs/current/sql-createrole.html for available privileges.
// +kubebuilder:validation:Enum=SUPERUSER;NOSUPERUSER;CREATEDB;NOCREATEDB;CREATEROLE;NOCREATEROLE;INHERIT;NOINHERIT;LOGIN;NOLOGIN;REPLICATION;NOREPLICATION;BYPASSRLS;NOBYPASSRLS
type RolePrivilege string

// RoleParameters define the desired state of a PostgreSQL role instance.
type RoleParameters struct {
	// Privileges to be granted.
	Privileges []RolePrivilege `json:"privileges"`

	// PasswordSecretRef references the secret that contains the password used
	// for this role. If no reference is given, a password will be auto-generated.
	// +optional
	PasswordSecretRef *xpv1.SecretKeySelector `json:"passwordSecretRef,omitempty"`
}

// A RoleObservation represents the observed state of a PostgreSQL role.
type RoleObservation struct {
}

// +kubebuilder:object:root=true

// A Role represents the declarative state of a PostgreSQL role.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Cluster
type Role struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RoleSpec   `json:"spec"`
	Status RoleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RoleList contains a list of Role
type RoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Role `json:"items"`
}
