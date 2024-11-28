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
	"context"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/reference"
)

// A GrantSpec defines the desired state of a Grant.
type GrantSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       GrantParameters `json:"forProvider"`
}

// GrantPrivilege represents a privilege to be granted
// +kubebuilder:validation:Pattern:=^[A-Z_ ]+$
type GrantPrivilege string

// If Privileges are specified, we should have at least one

// GrantPrivileges is a list of the privileges to be granted
// +kubebuilder:validation:MinItems:=1
type GrantPrivileges []GrantPrivilege

// ToStringSlice converts the slice of privileges to strings
func (gp *GrantPrivileges) ToStringSlice() []string {
	if gp == nil {
		return []string{}
	}
	out := make([]string, len(*gp))
	for i, v := range *gp {
		out[i] = string(v)
	}
	return out
}

// GrantParameters define the desired state of a MySQL grant instance.
type GrantParameters struct {
	// Privileges to be granted.
	// See https://mariadb.com/kb/en/grant/#database-privileges for available privileges.
	Privileges GrantPrivileges `json:"privileges"`

	// User this grant is for.
	// +optional
	User *string `json:"user,omitempty"`

	// UserRef references the user object this grant is for.
	// +immutable
	// +optional
	UserRef *xpv1.Reference `json:"userRef,omitempty"`

	// UserSelector selects a reference to a User this grant is for.
	// +immutable
	// +optional
	UserSelector *xpv1.Selector `json:"userSelector,omitempty"`

	// Tables this grant is for, default *.
	// +optional
	Table *string `json:"table,omitempty" default:"*"`

	// Database this grant is for, default *.
	// +optional
	Database *string `json:"database,omitempty" default:"*"`

	// DatabaseRef references the database object this grant it for.
	// +immutable
	// +optional
	DatabaseRef *xpv1.Reference `json:"databaseRef,omitempty"`

	// DatabaseSelector selects a reference to a Database this grant is for.
	// +immutable
	// +optional
	DatabaseSelector *xpv1.Selector `json:"databaseSelector,omitempty"`

	// BinLog defines whether the create, delete, update operations of this grant are propagated to replicas. Defaults to true
	// +optional
	BinLog *bool `json:"binlog,omitempty"`
}

// A GrantStatus represents the observed state of a Grant.
type GrantStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          GrantObservation `json:"atProvider,omitempty"`
}

// A GrantObservation represents the observed state of a MySQL grant.
type GrantObservation struct {
	// Privileges represents the applied privileges
	Privileges []string `json:"privileges,omitempty"`
}

// +kubebuilder:object:root=true

// A Grant represents the declarative state of a MySQL grant.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="ROLE",type="string",JSONPath=".spec.forProvider.user"
// +kubebuilder:printcolumn:name="DATABASE",type="string",JSONPath=".spec.forProvider.database"
// +kubebuilder:printcolumn:name="PRIVILEGES",type="string",JSONPath=".spec.forProvider.privileges"
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

// ResolveReferences of this Grant
func (mg *Grant) ResolveReferences(ctx context.Context, c client.Reader) error {
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

	// Resolve spec.forProvider.user
	rsp, err = r.Resolve(ctx, reference.ResolutionRequest{
		CurrentValue: reference.FromPtrValue(mg.Spec.ForProvider.User),
		Reference:    mg.Spec.ForProvider.UserRef,
		Selector:     mg.Spec.ForProvider.UserSelector,
		To:           reference.To{Managed: &User{}, List: &UserList{}},
		Extract:      reference.ExternalName(),
	})
	if err != nil {
		return errors.Wrap(err, "spec.forProvider.user")
	}
	mg.Spec.ForProvider.User = reference.ToPtrValue(rsp.ResolvedValue)
	mg.Spec.ForProvider.UserRef = rsp.ResolvedReference

	return nil
}
