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

// UsergroupParameters are the configurable fields of a Usergroup.
type UsergroupParameters struct {
	// +kubebuilder:validation:Pattern:=`^[^",\$\.'\+\-<>|\[\]\{\}\(\)!%*,/:;=\?@\\^~\x60a-z]+$`
	UsergroupName      string            `json:"usergroupName"`
	DisableUserAdmin   bool              `json:"disableUserAdmin,omitempty"`
	NoGrantToCreator   bool              `json:"noGrantToCreator,omitempty"`
	Parameters         map[string]string `json:"parameters,omitempty"`
	EnableParameterSet string            `json:"enableParameterSet,omitempty"`
}

// UsergroupObservation are the observable fields of a Usergroup.
type UsergroupObservation struct {
	UsergroupName    string            `json:"usergroupName"`
	DisableUserAdmin bool              `json:"disableUserAdmin,omitempty"`
	Parameters       map[string]string `json:"parameters,omitempty"`
}

// A UsergroupSpec defines the desired state of a Usergroup.
type UsergroupSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       UsergroupParameters `json:"forProvider"`
}

// A UsergroupStatus represents the observed state of a Usergroup.
type UsergroupStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          UsergroupObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A Usergroup is an example API type.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,sql}
type Usergroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   UsergroupSpec   `json:"spec"`
	Status UsergroupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// UsergroupList contains a list of Usergroup
type UsergroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Usergroup `json:"items"`
}

// Usergroup type metadata.
var (
	UsergroupKind             = reflect.TypeOf(Usergroup{}).Name()
	UsergroupGroupKind        = schema.GroupKind{Group: Group, Kind: UsergroupKind}.String()
	UsergroupKindAPIVersion   = UsergroupKind + "." + SchemeGroupVersion.String()
	UsergroupGroupVersionKind = SchemeGroupVersion.WithKind(UsergroupKind)
)

func init() {
	SchemeBuilder.Register(&Usergroup{}, &UsergroupList{})
}
