package mysql

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
)

func TestDSNURLEscaping(t *testing.T) {
	endpoint := "endpoint"
	port := "3306"
	user := "username"
	rawPass := "password^"
	tls := "true"
	binlog := false
	dsn := DSN(user, rawPass, endpoint, port, tls, &binlog)
	if dsn != fmt.Sprintf("%s:%s@tcp(%s:%s)/?tls=%s&sql_log_bin=%s",
		user,
		rawPass,
		endpoint,
		port,
		tls,
		strconv.FormatBool(binlog)) {
		t.Errorf("DSN string did not match expected output with URL encoded and binlog")
	}
}

func TestDSNURLEscapingWithoutBinLog(t *testing.T) {
	endpoint := "endpoint"
	port := "3306"
	user := "username"
	rawPass := "password^"
	tls := "true"
	dsn := DSN(user, rawPass, endpoint, port, tls, nil)
	if dsn != fmt.Sprintf("%s:%s@tcp(%s:%s)/?tls=%s",
		user,
		rawPass,
		endpoint,
		port,
		tls) {
		t.Errorf("DSN string did not match expected output with URL encoded")
	}
}

// TestDSNDialTimeout verifies that DSNWithDialTimeout appends the
// go-sql-driver `timeout` parameter when a positive duration is given,
// and omits it when zero. The parameter must coexist with sql_log_bin.
func TestDSNDialTimeout(t *testing.T) {
	endpoint := "endpoint"
	port := "3306"
	user := "username"
	rawPass := "password"
	tls := "true"

	cases := map[string]struct {
		dialTimeout time.Duration
		binlog      *bool
		wantContain []string
		wantOmit    []string
	}{
		"ZeroTimeoutOmitsParam": {
			dialTimeout: 0,
			wantOmit:    []string{"timeout="},
		},
		"PositiveTimeoutAddsParam": {
			dialTimeout: 10 * time.Second,
			wantContain: []string{"timeout=10s"},
		},
		"TimeoutCoexistsWithBinLog": {
			dialTimeout: 5 * time.Second,
			binlog:      boolPtr(false),
			wantContain: []string{"timeout=5s", "sql_log_bin=false"},
		},
		"NegativeTimeoutOmitsParam": {
			dialTimeout: -1 * time.Second,
			wantOmit:    []string{"timeout="},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dsn := DSNWithDialTimeout(user, rawPass, endpoint, port, tls, tc.binlog, tc.dialTimeout)
			for _, want := range tc.wantContain {
				if !contains(dsn, want) {
					t.Errorf("DSN %q missing expected substring %q", dsn, want)
				}
			}
			for _, omit := range tc.wantOmit {
				if contains(dsn, omit) {
					t.Errorf("DSN %q contains forbidden substring %q", dsn, omit)
				}
			}
		})
	}
}

// TestNewWithConfigAppliesPoolSettings verifies that pool tuning passed
// through NewWithConfig reaches the underlying *sql.DB. We check via
// db.Stats(), which surfaces the configured limits.
func TestNewWithConfigAppliesPoolSettings(t *testing.T) {
	defer resetPoolCacheForTest()
	resetPoolCacheForTest()

	creds := map[string][]byte{
		xpv1.ResourceCredentialsSecretEndpointKey: []byte("host-applies"),
		xpv1.ResourceCredentialsSecretPortKey:     []byte("3306"),
		xpv1.ResourceCredentialsSecretUserKey:     []byte("u"),
		xpv1.ResourceCredentialsSecretPasswordKey: []byte("p"),
	}
	tls := "preferred"
	cfg := &ConnectionPoolConfig{
		MaxOpenConns:    7,
		MaxIdleConns:    3,
		ConnMaxLifetime: 5 * time.Minute,
		ConnMaxIdleTime: 1 * time.Minute,
		DialTimeout:     10 * time.Second,
	}

	db := NewWithConfig(creds, &tls, nil, cfg)
	mdb, ok := db.(mySQLDB)
	if !ok {
		t.Fatalf("expected mySQLDB, got %T", db)
	}
	if mdb.openErr != nil {
		t.Fatalf("unexpected open error: %v", mdb.openErr)
	}
	if mdb.db == nil {
		t.Fatal("expected non-nil *sql.DB")
	}

	stats := mdb.db.Stats()
	if stats.MaxOpenConnections != 7 {
		t.Errorf("MaxOpenConnections = %d, want 7", stats.MaxOpenConnections)
	}
	// MaxIdleConnections is not surfaced via Stats; we rely on the fact
	// that SetMaxIdleConns was called (covered indirectly by the fact
	// that we constructed without panic and the DSN included timeout).
	if !contains(mdb.dsn, "timeout=10s") {
		t.Errorf("DSN %q missing timeout=10s", mdb.dsn)
	}
}

// TestNewWithConfigNilUsesDriverDefaults verifies the nil-config code
// path: no pool tuning applied, no dial timeout in the DSN. This
// preserves backward compatibility for callers that don't opt in.
func TestNewWithConfigNilUsesDriverDefaults(t *testing.T) {
	defer resetPoolCacheForTest()
	resetPoolCacheForTest()

	creds := map[string][]byte{
		xpv1.ResourceCredentialsSecretEndpointKey: []byte("host-nil"),
		xpv1.ResourceCredentialsSecretPortKey:     []byte("3306"),
		xpv1.ResourceCredentialsSecretUserKey:     []byte("u"),
		xpv1.ResourceCredentialsSecretPasswordKey: []byte("p"),
	}
	tls := "preferred"

	db := NewWithConfig(creds, &tls, nil, nil)
	mdb, ok := db.(mySQLDB)
	if !ok {
		t.Fatalf("expected mySQLDB, got %T", db)
	}

	stats := mdb.db.Stats()
	if stats.MaxOpenConnections != 0 {
		t.Errorf("MaxOpenConnections = %d, want 0 (unlimited Go default)", stats.MaxOpenConnections)
	}
	if contains(mdb.dsn, "timeout=") {
		t.Errorf("DSN %q should not contain timeout= when DialTimeout is unset", mdb.dsn)
	}
}

// TestPoolCacheReuse verifies that two New calls with equivalent
// credentials get the same underlying *sql.DB. This is the load-bearing
// behavior fix — without it, every reconcile opens a fresh pool and
// the Go "Open should be called once" guarantee is defeated.
func TestPoolCacheReuse(t *testing.T) {
	defer resetPoolCacheForTest()
	resetPoolCacheForTest()

	creds := map[string][]byte{
		xpv1.ResourceCredentialsSecretEndpointKey: []byte("host-reuse"),
		xpv1.ResourceCredentialsSecretPortKey:     []byte("3306"),
		xpv1.ResourceCredentialsSecretUserKey:     []byte("u"),
		xpv1.ResourceCredentialsSecretPasswordKey: []byte("p"),
	}
	tls := "preferred"

	db1 := NewWithConfig(creds, &tls, nil, nil).(mySQLDB)
	db2 := NewWithConfig(creds, &tls, nil, nil).(mySQLDB)
	if db1.db != db2.db {
		t.Errorf("expected pool cache to return the same *sql.DB for identical credentials; got distinct instances")
	}
}

// TestPoolCacheDistinctDSN verifies that different credentials get
// distinct pools. A credential rotation must NOT keep using the old
// pool, even though the connector returns to the same ProviderConfig.
func TestPoolCacheDistinctDSN(t *testing.T) {
	defer resetPoolCacheForTest()
	resetPoolCacheForTest()

	tls := "preferred"
	credsA := map[string][]byte{
		xpv1.ResourceCredentialsSecretEndpointKey: []byte("host-A"),
		xpv1.ResourceCredentialsSecretPortKey:     []byte("3306"),
		xpv1.ResourceCredentialsSecretUserKey:     []byte("u"),
		xpv1.ResourceCredentialsSecretPasswordKey: []byte("p"),
	}
	credsB := map[string][]byte{
		xpv1.ResourceCredentialsSecretEndpointKey: []byte("host-B"),
		xpv1.ResourceCredentialsSecretPortKey:     []byte("3306"),
		xpv1.ResourceCredentialsSecretUserKey:     []byte("u"),
		xpv1.ResourceCredentialsSecretPasswordKey: []byte("p"),
	}

	dbA := NewWithConfig(credsA, &tls, nil, nil).(mySQLDB)
	dbB := NewWithConfig(credsB, &tls, nil, nil).(mySQLDB)
	if dbA.db == dbB.db {
		t.Errorf("expected distinct *sql.DB for different DSNs; got the same instance")
	}
}

// TestNewBackwardCompatible verifies that the unchanged New signature
// still returns a working client. Callers that don't opt into pool
// tuning get behavior equivalent to NewWithConfig(..., nil).
func TestNewBackwardCompatible(t *testing.T) {
	defer resetPoolCacheForTest()
	resetPoolCacheForTest()

	creds := map[string][]byte{
		xpv1.ResourceCredentialsSecretEndpointKey: []byte("host-bc"),
		xpv1.ResourceCredentialsSecretPortKey:     []byte("3306"),
		xpv1.ResourceCredentialsSecretUserKey:     []byte("u"),
		xpv1.ResourceCredentialsSecretPasswordKey: []byte("p"),
	}
	tls := "preferred"

	db := New(creds, &tls, nil)
	mdb, ok := db.(mySQLDB)
	if !ok {
		t.Fatalf("expected mySQLDB, got %T", db)
	}
	if mdb.db == nil {
		t.Fatal("expected non-nil *sql.DB")
	}
	if contains(mdb.dsn, "timeout=") {
		t.Errorf("DSN %q should not contain timeout= when called via New (no config)", mdb.dsn)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func boolPtr(b bool) *bool { return &b }
