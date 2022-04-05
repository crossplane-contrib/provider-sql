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
type RolePrivilege struct {
	// SuperUser grants SUPERUSER privilege when true.
	// +optional
	SuperUser *bool `json:"superUser,omitempty"`

	// CreateDb grants CREATEDB when true, allowing the role to create databases.
	// +optional
	CreateDb *bool `json:"createDb,omitempty"`

	// CreateRole grants CREATEROLE when true, allowing this role to create other roles.
	// +optional
	CreateRole *bool `json:"createRole,omitempty"`

	// Login grants LOGIN when true, allowing the role to login to the server.
	// +optional
	Login *bool `json:"login,omitempty"`

	// Inherit grants INHERIT when true, allowing the role to inherit permissions
	// from other roles it is a member of.
	// +optional
	Inherit *bool `json:"inherit,omitempty"`

	// Replication grants REPLICATION when true, allowing the role to connect in replication mode.
	// +optional
	Replication *bool `json:"replication,omitempty"`

	// BypassRls grants BYPASSRLS when true, allowing the role to bypass row-level security policies.
	// +optional
	BypassRls *bool `json:"bypassRls,omitempty"`
}

// RoleParameters define the desired state of a PostgreSQL role instance.
type RoleParameters struct {
	// ConnectionLimit to be applied to the role.
	// +kubebuilder:validation:Min=-1
	// +optional
	ConnectionLimit *int32 `json:"connectionLimit,omitempty"`

	// Privileges to be granted.
	// +optional
	Privileges RolePrivilege `json:"privileges,omitempty"`

	// PasswordSecretRef references the secret that contains the password used
	// for this role. If no reference is given, a password will be auto-generated.
	// +optional
	PasswordSecretRef *xpv1.SecretKeySelector `json:"passwordSecretRef,omitempty"`

	// ConfigurationParameters to be applied to the role. If specified, any other configuration parameters set on the
	// role in the database will be reset.
	//
	// See https://www.postgresql.org/docs/current/runtime-config-client.html for some available configuration parameters.
	// +optional
	ConfigurationParameters *[]RoleConfigurationParameter `json:"configurationParameters,omitempty"`
}

// RoleConfigurationParameter is a role configuration parameter.
type RoleConfigurationParameter struct {
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

// A RoleObservation represents the observed state of a PostgreSQL role.
type RoleObservation struct {
	// PrivilegesAsClauses represents the applied privileges state, taking into account
	// any defaults applied by Postgres, and expressed as a list of ROLE PRIVILEGE clauses.
	PrivilegesAsClauses []string `json:"privilegesAsClauses,omitempty"`
	// ConfigurationParameters represents the applied configuration parameters for the PostgreSQL role.
	ConfigurationParameters *[]RoleConfigurationParameter `json:"configurationParameters,omitempty"`
}

// +kubebuilder:object:root=true

// A Role represents the declarative state of a PostgreSQL role.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="CONN LIMIT",type="integer",JSONPath=".spec.forProvider.connectionLimit"
// +kubebuilder:printcolumn:name="PRIVILEGES",type="string",JSONPath=".status.atProvider.privilegesAsClauses"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,sql}
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
