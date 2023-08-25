/*
Copyright 2022 The Crossplane Authors.

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
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// RoleParameters are the configurable fields of a Role.
type RoleParameters struct {
	// +kubebuilder:validation:Pattern:=`^[^",\$\.'\+\-<>|\[\]\{\}\(\)!%*,/:;=\?@\\^~\x60a-z]+$`
	RoleName string `json:"roleName"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	// +kubebuilder:validation:Pattern:=`^[^",\$\.'\+\-<>|\[\]\{\}\(\)!%*,/:;=\?@\\^~\x60a-z]+$`
	Schema string `json:"schema,omitempty"`

	LdapGroups []string `json:"ldapGroups,omitempty"`

	Privileges []string `json:"privileges,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	NoGrantToCreator bool `json:"noGrantToCreator,omitempty"`
}

// RoleObservation are the observable fields of a Role.
type RoleObservation struct {
	// +kubebuilder:validation:Pattern:=`^[^",\$\.'\+\-<>|\[\]\{\}\(\)!%*,/:;=\?@\\^~\x60a-z]+$`
	RoleName string `json:"roleName"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	// +kubebuilder:validation:Pattern:=`^[^",\$\.'\+\-<>|\[\]\{\}\(\)!%*,/:;=\?@\\^~\x60a-z]+$`
	Schema string `json:"schema,omitempty"`

	LdapGroups []string `json:"ldapGroups,omitempty"`

	Privileges []string `json:"privileges,omitempty"`
}

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

// +kubebuilder:object:root=true

// A Role is an example API type.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
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

// Role type metadata.
var (
	RoleKind             = reflect.TypeOf(Role{}).Name()
	RoleGroupKind        = schema.GroupKind{Group: Group, Kind: RoleKind}.String()
	RoleKindAPIVersion   = RoleKind + "." + SchemeGroupVersion.String()
	RoleGroupVersionKind = SchemeGroupVersion.WithKind(RoleKind)
)

func init() {
	SchemeBuilder.Register(&Role{}, &RoleList{})
}
