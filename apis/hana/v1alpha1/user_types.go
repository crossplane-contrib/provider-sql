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

type Authentication struct {
	Password Password `json:"password,omitempty"`
}

type Password struct {
	PasswordSecretRef        *xpv1.SecretKeySelector `json:"passwordSecretRef,omitempty"`
	ForceFirstPasswordChange bool                    `json:"forceFirstPasswordChange,omitempty"`
}

type Validity struct {
	// +kubebuilder:validation:Pattern:=`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`
	From string `json:"from,omitempty"`
	// +kubebuilder:validation:Pattern:=`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`
	Until string `json:"until"`
}

// UserParameters are the configurable fields of a User.
type UserParameters struct {
	// +kubebuilder:validation:Pattern:=`^[^",\$\.'\+\-<>|\[\]\{\}\(\)!%*,/:;=\?@\\^~\x60a-z]+$`
	Username       string            `json:"username"`
	RestrictedUser bool              `json:"restrictedUser,omitempty"`
	Authentication Authentication    `json:"authentication,omitempty"`
	Validity       Validity          `json:"validity,omitempty"`
	Parameters     map[string]string `json:"parameters,omitempty"`
	// +kubebuilder:validation:Pattern:=`^[^",\$\.'\+\-<>|\[\]\{\}\(\)!%*,/:;=\?@\\^~\x60a-z]+$`
	Usergroup string `json:"usergroup,omitempty"`
	// +kubebuilder:validation:Enum=LOCAL;LDAP
	// +kubebuilder:default=LOCAL
	LdapGroupAuthorization string `json:"ldapGroupAuthorization,omitempty"`
}

// UserObservation are the observable fields of a User.
type UserObservation struct {
	Username               string            `json:"username"`
	RestrictedUser         bool              `json:"restrictedUser,omitempty"`
	Authentication         Authentication    `json:"authentication,omitempty"`
	LastPasswordChangeTime string            `json:"lastPasswordChangeTime,omitempty"`
	PasswordSetTime        string            `json:"passwordSetTime"`
	Validity               Validity          `json:"validity,omitempty"`
	CreatedAt              string            `json:"createdAt,omitempty"`
	Parameters             map[string]string `json:"parameters,omitempty"`
	Usergroup              string            `json:"usergroup,omitempty"`
	LdapGroupAuthorization string            `json:"ldapGroupAuthorization,omitempty"`
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
