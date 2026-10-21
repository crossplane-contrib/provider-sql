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

	"github.com/pkg/errors"
)

const (
	sslModeVerifyCA   = "verify-ca"
	sslModeVerifyFull = "verify-full"

	errParseCA = "cannot parse CA bundle from tlsConfig.caCert"
)

// UsesCustomCA reports whether sslMode verifies the server certificate and
// therefore needs the CA bundle from tlsConfig.caCert.
func UsesCustomCA(sslMode string) bool {
	return sslMode == sslModeVerifyCA || sslMode == sslModeVerifyFull
}

// TLSConfig builds a client TLS configuration from a PEM bundle. The bundle
// may contain multiple CERTIFICATE blocks (roots and intermediates); every
// block is added to RootCAs.
//
// verify-full leaves certificate verification enabled and sets ServerName so
// crypto/tls checks the chain and the hostname. verify-ca skips the hostname
// check and verifies the chain against the bundle.
func TLSConfig(sslMode, serverName string, pemBytes []byte) (*tls.Config, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New(errParseCA)
	}

	cfg := &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	}
	switch sslMode {
	case sslModeVerifyFull:
		cfg.ServerName = serverName
	case sslModeVerifyCA:
		cfg.InsecureSkipVerify = true
		cfg.VerifyPeerCertificate = verifyChainOnly(pool)
	default:
		return nil, errors.Errorf("sslMode %q does not use a custom CA", sslMode)
	}
	return cfg, nil
}

// verifyChainOnly checks that the peer certificate chains to pool. It does not
// check the hostname. crypto/tls calls it when InsecureSkipVerify is set.
func verifyChainOnly(pool *x509.CertPool) func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("server presented no certificate")
		}
		certs := make([]*x509.Certificate, len(rawCerts))
		for i, asn1Data := range rawCerts {
			cert, err := x509.ParseCertificate(asn1Data)
			if err != nil {
				return errors.Wrap(err, "cannot parse server certificate")
			}
			certs[i] = cert
		}
		opts := x509.VerifyOptions{
			Roots:         pool,
			Intermediates: x509.NewCertPool(),
		}
		for _, cert := range certs[1:] {
			opts.Intermediates.AddCert(cert)
		}
		_, err := certs[0].Verify(opts)
		return err
	}
}
