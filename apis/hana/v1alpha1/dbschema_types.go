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

// DbSchemaParameters are the configurable fields of a DbSchema.
type DbSchemaParameters struct {
	ConfigurableField string `json:"configurableField"`
}

// DbSchemaObservation are the observable fields of a DbSchema.
type DbSchemaObservation struct {
	ObservableField string `json:"observableField,omitempty"`
}

// A DbSchemaSpec defines the desired state of a DbSchema.
type DbSchemaSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       DbSchemaParameters `json:"forProvider"`
}

// A DbSchemaStatus represents the observed state of a DbSchema.
type DbSchemaStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          DbSchemaObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A DbSchema is an example API type.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,sql}
type DbSchema struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DbSchemaSpec   `json:"spec"`
	Status DbSchemaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DbSchemaList contains a list of DbSchema
type DbSchemaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DbSchema `json:"items"`
}

// DbSchema type metadata.
var (
	DbSchemaKind             = reflect.TypeOf(DbSchema{}).Name()
	DbSchemaGroupKind        = schema.GroupKind{Group: Group, Kind: DbSchemaKind}.String()
	DbSchemaKindAPIVersion   = DbSchemaKind + "." + SchemeGroupVersion.String()
	DbSchemaGroupVersionKind = SchemeGroupVersion.WithKind(DbSchemaKind)
)

func init() {
	SchemeBuilder.Register(&DbSchema{}, &DbSchemaList{})
}
