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

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
)

// A ClusterProviderConfigSpec defines the desired state of a ClusterProviderConfig.
type ClusterProviderConfigSpec struct {
	// Credentials required to authenticate to this provider.
	Credentials ClusterProviderCredentials `json:"credentials"`

	// tls=true enables TLS / SSL encrypted connection to the server.
	// Use skip-verify if you want to use a self-signed or invalid certificate (server side)
	// or use preferred to use TLS only when advertised by the server. This is similar
	// to skip-verify, but additionally allows a fallback to a connection which is
	// not encrypted. Neither skip-verify nor preferred add any reliable security.
	// Alternatively, set tls=custom and provide a custom TLS configuration via the tlsConfig field.
	// +kubebuilder:validation:Enum="true";skip-verify;preferred;custom
	// +optional
	TLS *string `json:"tls"`

	// Optional TLS configuration for sql driver. Setting this field also requires the tls field to be set to custom.
	// +optional
	TLSConfig *TLSConfig `json:"tlsConfig"`
}

// ClusterProviderCredentials required to authenticate.
type ClusterProviderCredentials struct {
	// Source of the provider credentials.
	// +kubebuilder:validation:Enum=MySQLConnectionSecret
	Source MySQLConnectionSecretSource `json:"source"`

	// A CredentialsSecretRef is a reference to a MySQL connection secret
	// that contains the credentials that must be used to connect to the
	// provider. +optional
	ConnectionSecretRef xpv1.SecretReference `json:"connectionSecretRef,omitempty"`
}

// A ClusterProviderConfigStatus reflects the observed state of a ClusterProviderConfig.
type ClusterProviderConfigStatus struct {
	xpv1.ProviderConfigStatus `json:",inline"`
}

// +kubebuilder:object:root=true

// A ClusterProviderConfig configures a Template provider.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="SECRET-NAME",type="string",JSONPath=".spec.credentials.connectionSecretRef.name",priority=1
// +kubebuilder:resource:scope=Cluster,categories={crossplane,provider,sql}
type ClusterProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterProviderConfigSpec   `json:"spec"`
	Status ClusterProviderConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterProviderConfigList contains a list of ClusterProviderConfig.
type ClusterProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterProviderConfig `json:"items"`
}
