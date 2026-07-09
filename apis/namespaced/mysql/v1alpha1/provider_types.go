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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpv2 "github.com/crossplane/crossplane-runtime/v2/apis/common/v2"
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

	// ConnectionPool tunes the underlying database/sql connection pool
	// shared across reconciles. When omitted, Go's database/sql defaults
	// apply (unlimited open connections, 2 idle connections, no max
	// lifetime, no dial timeout). Configuring this is strongly
	// recommended for any non-trivial deployment — see upstream issues
	// #110, #195, and #220 for the failure modes the defaults can
	// produce under load.
	// +optional
	ConnectionPool *ConnectionPoolSpec `json:"connectionPool,omitempty"`
}

// ConnectionPoolSpec tunes the database/sql connection pool the provider
// uses to talk to MySQL. All fields are optional; a nil ConnectionPoolSpec
// (or a ConnectionPoolSpec with all fields zero) preserves Go's defaults.
type ConnectionPoolSpec struct {
	// MaxOpenConns bounds simultaneous in-use connections per pool. A
	// zero or unset value leaves Go's default (unlimited) in place,
	// which is the storm-prone behavior #110 calls out — set this to
	// bound the load offered to the database.
	// +optional
	MaxOpenConns *int `json:"maxOpenConns,omitempty"`

	// MaxIdleConns bounds idle pool size. A zero or unset value leaves
	// Go's default (2) in place. Negative values disable idle
	// connection retention.
	// +optional
	MaxIdleConns *int `json:"maxIdleConns,omitempty"`

	// ConnMaxLifetime caps how long a connection may be reused. An
	// unset or zero value allows connections to live forever — usually
	// undesirable behind load balancers and managed proxies that may
	// close long-lived connections from their side. Recommended: a few
	// minutes.
	// +optional
	ConnMaxLifetime *metav1.Duration `json:"connMaxLifetime,omitempty"`

	// ConnMaxIdleTime caps how long a connection may sit idle in the
	// pool before being closed. An unset or zero value allows idle
	// connections to live forever.
	// +optional
	ConnMaxIdleTime *metav1.Duration `json:"connMaxIdleTime,omitempty"`

	// DialTimeout bounds the TCP connect and TLS handshake that open
	// new connections to the database. An unset or zero value leaves
	// the go-sql-driver default (no timeout) in place. Recommended
	// for any deployment behind a proxy that can hang on connect.
	// +optional
	DialTimeout *metav1.Duration `json:"dialTimeout,omitempty"`
}

// ToPoolValues unpacks the optional pool spec into plain values
// suitable for mysql.NewConnectionPoolConfig. Returns the zero value
// for any field not set, including when the receiver itself is nil.
func (s *ConnectionPoolSpec) ToPoolValues() (maxOpen, maxIdle int, lifetime, idleTime, dialTimeout time.Duration) {
	if s == nil {
		return 0, 0, 0, 0, 0
	}
	if s.MaxOpenConns != nil {
		maxOpen = *s.MaxOpenConns
	}
	if s.MaxIdleConns != nil {
		maxIdle = *s.MaxIdleConns
	}
	if s.ConnMaxLifetime != nil {
		lifetime = s.ConnMaxLifetime.Duration
	}
	if s.ConnMaxIdleTime != nil {
		idleTime = s.ConnMaxIdleTime.Duration
	}
	if s.DialTimeout != nil {
		dialTimeout = s.DialTimeout.Duration
	}
	return
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

type MySQLConnectionSecretSource string

const (
	// CredentialsSourceMySQLConnectionSecret indicates that a provider
	// should acquire credentials from a connection secret written by a managed
	// resource that represents a MySQL server.
	CredentialsSourceMySQLConnectionSecret MySQLConnectionSecretSource = "MySQLConnectionSecret"
)

// ProviderCredentials required to authenticate.
type ProviderCredentials struct {
	// Source of the provider credentials.
	// +kubebuilder:validation:Enum=MySQLConnectionSecret
	Source MySQLConnectionSecretSource `json:"source"`

	// A CredentialsSecretRef is a reference to a MySQL connection secret
	// that contains the credentials that must be used to connect to the
	// provider. +optional
	ConnectionSecretRef xpv1.LocalSecretReference `json:"connectionSecretRef,omitempty"`

	// SecretKeyMapping allows overriding the default secret key names used
	// to read credentials from the connection secret. When not specified,
	// standard Crossplane keys are used: "endpoint", "port", "username", "password".
	// +optional
	SecretKeyMapping *SecretKeyMapping `json:"secretKeyMapping,omitempty"`
}

// SecretKeyMapping allows overriding the default secret key names used to
// read credentials from the connection secret.
type SecretKeyMapping struct {
	// Endpoint overrides the key used to read the host/endpoint. Default: "endpoint".
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// Port overrides the key used to read the port. Default: "port".
	// +optional
	Port string `json:"port,omitempty"`
	// Username overrides the key used to read the username. Default: "username".
	// +optional
	Username string `json:"username,omitempty"`
	// Password overrides the key used to read the password. Default: "password".
	// +optional
	Password string `json:"password,omitempty"`
}

// ToMap converts the mapping to a map[string]string suitable for
// xsql.RemapCredentialKeys. Returns nil when the receiver is nil.
func (m *SecretKeyMapping) ToMap() map[string]string {
	if m == nil {
		return nil
	}
	mapping := make(map[string]string, 4)
	if m.Endpoint != "" {
		mapping["endpoint"] = m.Endpoint
	}
	if m.Port != "" {
		mapping["port"] = m.Port
	}
	if m.Username != "" {
		mapping["username"] = m.Username
	}
	if m.Password != "" {
		mapping["password"] = m.Password
	}
	return mapping
}

// A ProviderConfigStatus reflects the observed state of a ProviderConfig.
type ProviderConfigStatus struct {
	xpv1.ProviderConfigStatus `json:",inline"`
}

// +kubebuilder:object:root=true

// A ProviderConfig configures a Template provider.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="SECRET-NAME",type="string",JSONPath=".spec.credentials.connectionSecretRef.name",priority=1
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,provider,sql}
type ProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProviderConfigSpec   `json:"spec"`
	Status ProviderConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// A ProviderConfigList contains a list of ProviderConfig.
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
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,provider,sql}
type ProviderConfigUsage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	xpv2.TypedProviderConfigUsage `json:",inline"`
}

// +kubebuilder:object:root=true

// ProviderConfigUsageList contains a list of ProviderConfigUsage
type ProviderConfigUsageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfigUsage `json:"items"`
}
