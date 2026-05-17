package xsql

import (
	"testing"

	"github.com/google/go-cmp/cmp"
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
