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

// A ProviderConfigSpec defines the desired state of a ProviderConfig.
type ProviderConfigSpec struct {
	// Credentials required to authenticate to this provider.
	Credentials ProviderCredentials `json:"credentials"`
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

// TLSConfig defines the TLS configuration for the provider when tls=custom.
type TLSConfig struct {
	CACert             TLSSecret `json:"caCert,omitempty"`
	ClientCert         TLSSecret `json:"clientCert,omitempty"`
	ClientKey          TLSSecret `json:"clientKey,omitempty"`
	InsecureSkipVerify bool      `json:"insecureSkipVerify,omitempty"`
}

// TLSSecret defines a reference to a K8s secret and its specific internal key that contains the TLS cert/keys in PEM format.
type TLSSecret struct {
	SecretRef xpv1.SecretKeySelector `json:"secretRef,omitempty"`
}

const (
	// CredentialsSourceMySQLConnectionSecret indicates that a provider
	// should acquire credentials from a connection secret written by a managed
	// resource that represents a MySQL server.
	CredentialsSourceMySQLConnectionSecret xpv1.CredentialsSource = "MySQLConnectionSecret"
)

// ProviderCredentials required to authenticate.
type ProviderCredentials struct {
	// Source of the provider credentials.
	// +kubebuilder:validation:Enum=MySQLConnectionSecret
	Source xpv1.CredentialsSource `json:"source"`

	// A CredentialsSecretRef is a reference to a MySQL connection secret
	// that contains the credentials that must be used to connect to the
	// provider. +optional
	ConnectionSecretRef *xpv1.SecretReference `json:"connectionSecretRef,omitempty"`
}

// A ProviderConfigStatus reflects the observed state of a ProviderConfig.
type ProviderConfigStatus struct {
	xpv1.ProviderConfigStatus `json:",inline"`
}

// +kubebuilder:object:root=true

// A ProviderConfig configures a Template provider.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="SECRET-NAME",type="string",JSONPath=".spec.credentialsSecretRef.name",priority=1
// +kubebuilder:resource:scope=Cluster,categories={crossplane,provider,sql}
type ProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProviderConfigSpec   `json:"spec"`
	Status ProviderConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ProviderConfigList contains a list of ProviderConfig.
type ProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfig `json:"items"`
}

// +kubebuilder:object:root=true

// A ProviderConfigUsage indicates that a resource is using a ProviderConfig.
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="CONFIG-NAME",type="string",JSONPath=".providerConfigRef.name"
// +kubebuilder:printcolumn:name="RESOURCE-KIND",type="string",JSONPath=".resourceRef.kind"
// +kubebuilder:printcolumn:name="RESOURCE-NAME",type="string",JSONPath=".resourceRef.name"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,provider,sql}
type ProviderConfigUsage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	xpv1.ProviderConfigUsage `json:",inline"`
}

// +kubebuilder:object:root=true

// ProviderConfigUsageList contains a list of ProviderConfigUsage
type ProviderConfigUsageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfigUsage `json:"items"`
}
