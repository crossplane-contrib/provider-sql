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

// A UserSpec defines the desired state of a Database.
type UserSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       UserParameters `json:"forProvider"`
}

// A UserStatus represents the observed state of a User.
type UserStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          UserObservation `json:"atProvider,omitempty"`
}

// UserParameters define the desired state of a MySQL user instance.
type UserParameters struct {
	// PasswordSecretRef references the secret that contains the password used
	// for this user. If no reference is given, a password will be auto-generated.
	// +optional
	PasswordSecretRef *xpv1.SecretKeySelector `json:"passwordSecretRef,omitempty"`

	// ResourceOptions sets account specific resource limits.
	// See https://dev.mysql.com/doc/refman/8.0/en/user-resources.html
	// +optional
	ResourceOptions *ResourceOptions `json:"resourceOptions,omitempty"`

	// BinLog defines whether the create, delete, update operations of this user are propagated to replicas. Defaults to true
	// +optional
	BinLog *bool `json:"binlog,omitempty"`
}

// ResourceOptions define the account specific resource limits.
type ResourceOptions struct {
	// MaxQueriesPerHour sets the number of queries an account can issue per hour
	// +optional
	MaxQueriesPerHour *int `json:"maxQueriesPerHour,omitempty"`

	// MaxUpdatesPerHour sets the number of updates an account can issue per hour
	// +optional
	MaxUpdatesPerHour *int `json:"maxUpdatesPerHour,omitempty"`

	// MaxConnectionsPerHour sets the number of times an account can connect to the server per hour
	// +optional
	MaxConnectionsPerHour *int `json:"maxConnectionsPerHour,omitempty"`

	// MaxUserConnections sets The number of simultaneous connections to the server by an account
	// +optional
	MaxUserConnections *int `json:"maxUserConnections,omitempty"`
}

// A UserObservation represents the observed state of a MySQL user.
type UserObservation struct {
	// ResourceOptionsAsClauses represents the applied resource options
	ResourceOptionsAsClauses []string `json:"resourceOptionsAsClauses,omitempty"`
}

// +kubebuilder:object:root=true

// A User represents the declarative state of a MySQL user.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,sql}
type User struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   UserSpec   `json:"spec"`
	Status UserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// UserList contains a list of User
type UserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []User `json:"items"`
}
