package mssql

import (
	"testing"

	"github.com/microsoft/go-mssqldb/azuread"
)

func TestAzureEntraSID(t *testing.T) {
	got, err := AzureEntraSID("00112233-4455-6677-8899-aabbccddeeff")
	if err != nil {
		t.Fatalf("AzureEntraSID returned an unexpected error: %v", err)
	}
	const want = "0x33221100554477668899aabbccddeeff"
	if got != want {
		t.Fatalf("AzureEntraSID = %q, want %q", got, want)
	}
}

func TestNewAzureWorkloadIdentity(t *testing.T) {
	creds := map[string][]byte{
		"endpoint":                       []byte("server.database.windows.net"),
		"port":                           []byte("1433"),
		"provider-sql.io/authentication": []byte("AzureWorkloadIdentity"),
	}
	db := New(creds, "appdb").(mssqlDB)
	if db.driver != azuread.DriverName {
		t.Fatalf("driver = %q, want %q", db.driver, azuread.DriverName)
	}
	const want = "sqlserver://server.database.windows.net:1433?database=appdb&fedauth=ActiveDirectoryWorkloadIdentity"
	if db.dsn != want {
		t.Fatalf("dsn = %q, want %q", db.dsn, want)
	}
	if _, found := db.GetConnectionDetails("app", "")["password"]; found {
		t.Fatal("Azure workload identity connection details must not contain a password")
	}
}
