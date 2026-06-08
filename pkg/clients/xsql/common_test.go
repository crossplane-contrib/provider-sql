package xsql

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemapCredentialKeys(t *testing.T) {
	baseData := map[string][]byte{
		"host":     []byte("pg.example.com"),
		"port":     []byte("5432"),
		"user":     []byte("admin"),
		"pass":     []byte("secret"),
		"endpoint": []byte("original-endpoint"),
	}

	cases := map[string]struct {
		data    map[string][]byte
		mapping map[string]string
		want    map[string][]byte
	}{
		"NilMapping": {
			data:    baseData,
			mapping: nil,
			want:    baseData,
		},
		"EmptyMapping": {
			data:    baseData,
			mapping: map[string]string{},
			want:    baseData,
		},
		"PartialMapping": {
			data: baseData,
			mapping: map[string]string{
				"endpoint": "host",
				"username": "user",
			},
			want: map[string][]byte{
				"host":     []byte("pg.example.com"),
				"port":     []byte("5432"),
				"user":     []byte("admin"),
				"pass":     []byte("secret"),
				"endpoint": []byte("pg.example.com"),
				"username": []byte("admin"),
			},
		},
		"FullMapping": {
			data: baseData,
			mapping: map[string]string{
				"endpoint": "host",
				"port":     "port",
				"username": "user",
				"password": "pass",
			},
			want: map[string][]byte{
				"host":     []byte("pg.example.com"),
				"port":     []byte("5432"),
				"user":     []byte("admin"),
				"pass":     []byte("secret"),
				"endpoint": []byte("pg.example.com"),
				"username": []byte("admin"),
				"password": []byte("secret"),
			},
		},
		"CustomKeyNotPresent": {
			data: baseData,
			mapping: map[string]string{
				"endpoint": "nonexistent",
			},
			want: map[string][]byte{
				"host":     []byte("pg.example.com"),
				"port":     []byte("5432"),
				"user":     []byte("admin"),
				"pass":     []byte("secret"),
				"endpoint": []byte("original-endpoint"),
			},
		},
		"DoesNotMutateOriginal": {
			data: map[string][]byte{
				"host": []byte("pg.example.com"),
			},
			mapping: map[string]string{
				"endpoint": "host",
			},
			want: map[string][]byte{
				"host":     []byte("pg.example.com"),
				"endpoint": []byte("pg.example.com"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := RemapCredentialKeys(tc.data, tc.mapping)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("RemapCredentialKeys(...): -want, +got:\n%s", diff)
			}
		})
	}

	// Verify DoesNotMutateOriginal: original data must not have "endpoint"
	t.Run("DoesNotMutateOriginal_verify", func(t *testing.T) {
		original := map[string][]byte{
			"host": []byte("pg.example.com"),
		}
		RemapCredentialKeys(original, map[string]string{"endpoint": "host"})
		if _, ok := original["endpoint"]; ok {
			t.Error("RemapCredentialKeys mutated the original data map")
		}
	})
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    int
		wantErr bool
	}{
		// MySQL-style versions
		{name: "MySQL_Standard", version: "8.0.35", want: 80035},
		{name: "MySQL_WithSuffix", version: "8.0.35-0ubuntu0.22.04.1", want: 80035},
		{name: "MySQL_MajorMinorOnly", version: "8.0", want: 80000},
		{name: "MySQL_9", version: "9.1.0", want: 90100},
		{name: "MariaDB", version: "10.11.6-MariaDB", want: 101106},

		// MSSQL-style versions (build number > 99, ignored)
		{name: "MSSQL_2022", version: "16.0.1125.1", want: 160000},
		{name: "MSSQL_2019", version: "15.0.4355.3", want: 150000},
		{name: "MSSQL_NonZeroMinor", version: "16.5.100.1", want: 160500},

		// PostgreSQL-style (for reference, PG uses its own query)
		{name: "ThreeDigitPatch", version: "14.2.1", want: 140201},
		{name: "PatchExactly99", version: "14.0.99", want: 140099},
		{name: "PatchOver99", version: "14.0.100", want: 140000},

		// Errors
		{name: "InvalidFormat", version: "invalid", wantErr: true},
		{name: "EmptyString", version: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseVersion(tt.version)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
