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

// Authentication includes different authentication methods
type Authentication struct {
	Password Password `json:"password,omitempty"`
}

// Password authentication type
type Password struct {
	PasswordSecretRef        *xpv1.SecretKeySelector `json:"passwordSecretRef,omitempty"`
	ForceFirstPasswordChange bool                    `json:"forceFirstPasswordChange,omitempty"`
}

// UserParameters are the configurable fields of a User.
type UserParameters struct {
	// +kubebuilder:validation:Pattern:=`^[^",\$\.'\+\-<>|\[\]\{\}\(\)!%*,/:;=\?@\\^~\x60a-z]+$`
	Username string `json:"username"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	RestrictedUser bool `json:"restrictedUser,omitempty"`

	Authentication Authentication `json:"authentication,omitempty"`

	Privileges []string `json:"privileges,omitempty"`

	Roles []string `json:"roles,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	Parameters map[string]string `json:"parameters,omitempty"`

	// +kubebuilder:validation:Pattern:=`^[^",\$\.'\+\-<>|\[\]\{\}\(\)!%*,/:;=\?@\\^~\x60a-z]+$`
	// +kubebuilder:default:=DEFAULT
	Usergroup string `json:"usergroup,omitempty" default:"DEFAULT"`
}

// UserObservation are the observable fields of a User.
type UserObservation struct {
	// +kubebuilder:validation:Pattern:=`^[^",\$\.'\+\-<>|\[\]\{\}\(\)!%*,/:;=\?@\\^~\x60a-z]+$`
	Username string `json:"username"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	RestrictedUser bool `json:"restrictedUser,omitempty"`

	Authentication Authentication `json:"authentication,omitempty"`

	LastPasswordChangeTime string `json:"lastPasswordChangeTime,omitempty"`

	CreatedAt string `json:"createdAt,omitempty"`

	Privileges []string `json:"privileges,omitempty"`

	Roles []string `json:"roles,omitempty"`

	Parameters map[string]string `json:"parameters,omitempty"`

	// +kubebuilder:validation:Pattern:=`^[^",\$\.'\+\-<>|\[\]\{\}\(\)!%*,/:;=\?@\\^~\x60a-z]+$`
	// +kubebuilder:default:=DEFAULT
	Usergroup string `json:"usergroup,omitempty" default:"DEFAULT"`
}

// A UserSpec defines the desired state of a User.
type UserSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       UserParameters `json:"forProvider"`
}

// A UserStatus represents the observed state of a User.
type UserStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          UserObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A User is an example API type.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
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

// User type metadata.
var (
	UserKind             = reflect.TypeOf(User{}).Name()
	UserGroupKind        = schema.GroupKind{Group: Group, Kind: UserKind}.String()
	UserKindAPIVersion   = UserKind + "." + SchemeGroupVersion.String()
	UserGroupVersionKind = SchemeGroupVersion.WithKind(UserKind)
)

func init() {
	SchemeBuilder.Register(&User{}, &UserList{})
}