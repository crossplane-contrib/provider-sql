package xsql

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"database/sql"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
)

// A Query that may be run against a DB.
type Query struct {
	String     string
	Parameters []interface{}
}

// A DB client.
type DB interface {
	Exec(ctx context.Context, q Query) error
	ExecTx(cts context.Context, ql []Query) error
	Scan(ctx context.Context, q Query, dest ...interface{}) error
	Query(ctx context.Context, q Query) (*sql.Rows, error)
	GetConnectionDetails(username, password string) managed.ConnectionDetails
	GetServerVersion(ctx context.Context) (int, error)
}

// ParseVersion parses a database version string into an integer
// encoded as major*10000 + minor*100 + patch.
// Suffixes after '-' are stripped (e.g. "8.0.35-ubuntu" → 80035).
// Patch values above 99 are ignored (e.g. MSSQL build numbers).
func ParseVersion(version string) (int, error) {
	if idx := strings.IndexByte(version, '-'); idx >= 0 {
		version = version[:idx]
	}

	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return 0, fmt.Errorf("unexpected version format: %s", version)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("parsing major version %q: %w", parts[0], err)
	}

	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("parsing minor version %q: %w", parts[1], err)
	}

	var patch int
	if len(parts) == 3 {
		p, err := strconv.Atoi(parts[2])
		if err == nil && p <= 99 {
			patch = p
		}
	}

	return major*10000 + minor*100 + patch, nil
}

// IsNoRows returns true if the supplied error indicates no rows were returned.
func IsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// RemapCredentialKeys returns a copy of data where standard Crossplane
// connection secret keys ("endpoint", "port", "username", "password")
// are populated from the custom key names defined in mapping.
// Entries with empty values are skipped. A nil or empty mapping returns
// data unchanged.
func RemapCredentialKeys(data map[string][]byte, mapping map[string]string) map[string][]byte {
	if len(mapping) == 0 {
		return data
	}
	remapped := make(map[string][]byte, len(data))
	for k, v := range data {
		remapped[k] = v
	}
	for standardKey, customKey := range mapping {
		if customKey != "" {
			if val, ok := data[customKey]; ok {
				remapped[standardKey] = val
			}
		}
	}
	return remapped
}
