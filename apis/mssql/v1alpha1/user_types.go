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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// UserTypes
const (
	UserTypeAD    = "AD"
	UserTypeLocal = "Local"
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

// UserParameters define the desired state of a MSSQL user instance.
type UserParameters struct {
	// Database allows you to specify the name of the Database the USER is created for.
	// +crossplane:generate:reference:type=Database
	Database *string `json:"database,omitempty"`
	// DatabaseRef allows you to specify custom resource name of the Database the USER is created for.
	// to fill Database field.
	DatabaseRef *xpv1.Reference `json:"databaseRef,omitempty"`
	// DatabaseSelector allows you to use selector constraints to select a Database the USER is created for.
	DatabaseSelector *xpv1.Selector `json:"databaseSelector,omitempty"`
	// PasswordSecretRef references the secret that contains the password used
	// for this user. If no reference is given, a password will be auto-generated.
	// +optional
	PasswordSecretRef *xpv1.SecretKeySelector `json:"passwordSecretRef,omitempty"`
	// LoginDatabase allows you to specify the name of the Database to be used to create the user LOGIN in (normally master).
	// +crossplane:generate:reference:type=Database
	LoginDatabase *string `json:"loginDatabase,omitempty"`
	// DatabaseRef allows you to specify custom resource name of the Database to be used to create the user LOGIN in (normally master).
	// to fill Database field.
	LoginDatabaseRef *xpv1.Reference `json:"loginDatabaseRef,omitempty"`
	// DatabaseSelector allows you to use selector constraints to select a Database to be used to create the user LOGIN in (normally master).
	LoginDatabaseSelector *xpv1.Selector `json:"loginDatabaseSelector,omitempty"`
	// The type of user to create.  Defaults to Local.  Can be either Local or AD.
	Type *string `json:"type,omitempty"`
}

// A UserObservation represents the observed state of a MSSQL user.
type UserObservation struct {
}

// +kubebuilder:object:root=true

// A User represents the declarative state of a MSSQL user.
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
