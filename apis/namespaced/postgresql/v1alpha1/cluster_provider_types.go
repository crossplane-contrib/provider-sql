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

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

// A ClusterProviderConfigSpec defines the desired state of a ClusterProviderConfig.
// +kubebuilder:validation:XValidation:rule="!has(self.tlsConfig) || (has(self.sslMode) && self.sslMode in ['verify-ca', 'verify-full'])",message="tlsConfig requires sslMode verify-ca or verify-full"
type ClusterProviderConfigSpec struct {
	// Credentials required to authenticate to this provider.
	Credentials ClusterProviderCredentials `json:"credentials"`
	// Defines the database name used to set up a connection to the provided
	// PostgreSQL instance. Same as PGDATABASE environment variable.
	// +kubebuilder:default="postgres"
	DefaultDatabase string `json:"defaultDatabase,omitempty"`
	// Defines the SSL mode used to set up a connection to the provided
	// PostgreSQL instance
	// +kubebuilder:validation:Enum=disable;require;verify-ca;verify-full
	// +kubebuilder:default=verify-full
	// +kubebuilder:validation:Optional
	SSLMode *string `json:"sslMode,omitempty"`

	// TLSConfig selects the CA bundle used to verify the PostgreSQL server.
	// When set, sslMode must be verify-ca or verify-full.
	// +optional
	TLSConfig *ClusterTLSConfig `json:"tlsConfig,omitempty"`
}

// ClusterTLSConfig selects the CA bundle for a ClusterProviderConfig.
type ClusterTLSConfig struct {
	// CACert contains the PEM-encoded certificates trusted for this connection.
	CACert ClusterTLSSecret `json:"caCert"`
}

// ClusterTLSSecret selects a Secret key for a cluster-scoped configuration.
type ClusterTLSSecret struct {
	// SecretRef selects the CA bundle. Namespace, name and key are required.
	SecretRef xpv2.SecretKeySelector `json:"secretRef"`
}

// ClusterProviderCredentials required to authenticate.
type ClusterProviderCredentials struct {
	// Source of the provider credentials.
	// +kubebuilder:validation:Enum=PostgreSQLConnectionSecret;AWSIAMAuth
	Source PostgreSQLConnectionSource `json:"source"`

	// A CredentialsSecretRef is a reference to a PostgreSQL connection secret
	// that contains the credentials that must be used to connect to the
	// provider. +optional
	ConnectionSecretRef xpv2.SecretReference `json:"connectionSecretRef,omitempty"`

	// SecretKeyMapping allows overriding the default secret key names used
	// to read credentials from the connection secret. When not specified,
	// standard Crossplane keys are used: "endpoint", "port", "username", "password".
	// +optional
	SecretKeyMapping *SecretKeyMapping `json:"secretKeyMapping,omitempty"`

	// Region is the AWS region used to generate the IAM authentication token
	// when source is AWSIAMAuth. When unset it falls back to a "region" key in
	// the connection secret, then to the controller's AWS environment.
	// +optional
	Region *string `json:"region,omitempty"`
}

// A ClusterProviderConfigStatus reflects the observed state of a ClusterProviderConfig.
type ClusterProviderConfigStatus struct {
	xpv2.ProviderConfigStatus `json:",inline"`
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
