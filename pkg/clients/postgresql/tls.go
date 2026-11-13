/*
Copyright 2026 The Crossplane Authors.

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

package postgresql

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
)

// TLSConfig builds server verification using only the selected CA bundle.
// verify-full checks both the certificate chain and the endpoint hostname.
// verify-ca checks the chain without checking the hostname.
func TLSConfig(mode, endpoint string, ca []byte) (*tls.Config, error) {
	if mode != "verify-ca" && mode != "verify-full" {
		return nil, fmt.Errorf("tlsConfig.caCert requires sslMode verify-ca or verify-full, got %q", mode)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("tlsConfig.caCert does not contain a valid PEM certificate")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: endpoint, RootCAs: roots}
	if mode == "verify-ca" {
		cfg.InsecureSkipVerify = true //nolint:gosec // The callback verifies the chain; this mode deliberately omits hostname verification.
		cfg.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("PostgreSQL server presented no certificate")
			}
			intermediates := x509.NewCertPool()
			for _, cert := range state.PeerCertificates[1:] {
				intermediates.AddCert(cert)
			}
			_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates})
			return err
		}
	}
	return cfg, nil
}
