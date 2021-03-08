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

// DatabaseParameters are the configurable fields of a Database.
type DatabaseParameters struct {
	// The role name of the user who will own the new database, or DEFAULT to
	// use the default (namely, the user executing the command). To create a
	// database owned by another role, you must be a direct or indirect member
	// of that role, or be a superuser.
	Owner *string `json:"owner,omitempty"`

	// The name of the template from which to create the new database, or
	// DEFAULT to use the default template (template1).
	Template *string `json:"template,omitempty"`

	// Character set encoding to use in the new database. Specify a string
	// constant (e.g., 'SQL_ASCII'), or an integer encoding number, or DEFAULT
	// to use the default encoding (namely, the encoding of the template
	// database). The character sets supported by the PostgreSQL server are
	// described in Section 23.3.1. See below for additional restrictions.
	Encoding *string `json:"encoding,omitempty"`

	// Collation order (LC_COLLATE) to use in the new database. This affects the
	// sort order applied to strings, e.g. in queries with ORDER BY, as well as
	// the order used in indexes on text columns. The default is to use the
	// collation order of the template database. See below for additional
	// restrictions.
	LCCollate *string `json:"lcCollate,omitempty"`

	// Character classification (LC_CTYPE) to use in the new database. This
	// affects the categorization of characters, e.g. lower, upper and digit.
	// The default is to use the character classification of the template
	// database. See below for additional restrictions.
	LCCType *string `json:"lcCType,omitempty"`

	// The name of the tablespace that will be associated with the new database,
	// or DEFAULT to use the template database's tablespace. This tablespace
	// will be the default tablespace used for objects created in this database.
	// See CREATE TABLESPACE for more information.
	Tablespace *string `json:"tablespace,omitempty"`

	// If false then no one can connect to this database. The default is true,
	// allowing connections (except as restricted by other mechanisms, such as
	// GRANT/REVOKE CONNECT).
	AllowConnections *bool `json:"allowConnections,omitempty"`

	// How many concurrent connections can be made to this database. -1 (the
	// default) means no limit.
	ConnectionLimit *int `json:"connectionLimit,omitempty"`

	// If true, then this database can be cloned by any user with CREATEDB
	// privileges; if false (the default), then only superusers or the owner of
	// the database can clone it.
	IsTemplate *bool `json:"isTemplate,omitempty"`
}

// A DatabaseSpec defines the desired state of a Database.
type DatabaseSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       DatabaseParameters `json:"forProvider"`
}

// A DatabaseStatus represents the observed state of a Database.
type DatabaseStatus struct {
	xpv1.ResourceStatus `json:",inline"`
}

// +kubebuilder:object:root=true

// A Database represents the declarative state of a PostgreSQL database.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,sql}
type Database struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DatabaseSpec   `json:"spec"`
	Status DatabaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DatabaseList contains a list of Database
type DatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Database `json:"items"`
}
