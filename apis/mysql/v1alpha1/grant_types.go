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

	runtimev1alpha1 "github.com/crossplane/crossplane-runtime/apis/core/v1alpha1"
	"github.com/crossplane/crossplane-runtime/pkg/reference"
)

// A GrantSpec defines the desired state of a Grant.
type GrantSpec struct {
	runtimev1alpha1.ResourceSpec `json:",inline"`
	ForProvider                  GrantParameters `json:"forProvider"`
}

// GrantParameters define the desired state of a MySQL grant instance.
type GrantParameters struct {
	// Privileges to be granted.
	// See https://mariadb.com/kb/en/grant/#database-privileges for available privileges.
	Privileges []string `json:"privileges"`

	// User this grant is for.
	// +optional
	User *string `json:"user,omitempty"`

	// UserRef references the user object this grant is for.
	// +optional
	UserRef *runtimev1alpha1.Reference `json:"userRef,omitempty"`

	// Database this grant is for.
	// +optional
	Database *string `json:"database,omitempty"`

	// DatabaseRef references the database object this grant it for.
	// +optional
	DatabaseRef *runtimev1alpha1.Reference `json:"databaseRef,omitempty"`
}

// A GrantStatus represents the observed state of a Grant.
type GrantStatus struct {
	runtimev1alpha1.ResourceStatus `json:",inline"`
}

// +kubebuilder:object:root=true

// A Grant represents the declarative state of a MySQL grant.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Cluster
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
		To:           reference.To{Managed: &Database{}},
		Extract:      reference.ExternalName(),
	})
	if err != nil {
		return errors.Wrap(err, "spec.forProvider.database")
	}
	mg.Spec.ForProvider.Database = reference.ToPtrValue(rsp.ResolvedValue)

	// Resolve spec.forProvider.user
	rsp, err = r.Resolve(ctx, reference.ResolutionRequest{
		CurrentValue: reference.FromPtrValue(mg.Spec.ForProvider.User),
		Reference:    mg.Spec.ForProvider.UserRef,
		To:           reference.To{Managed: &User{}},
		Extract:      reference.ExternalName(),
	})
	if err != nil {
		return errors.Wrap(err, "spec.forProvider.user")
	}
	mg.Spec.ForProvider.User = reference.ToPtrValue(rsp.ResolvedValue)

	return nil
}
