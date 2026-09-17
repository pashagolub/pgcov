package database_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cybertec-postgresql/pgcov/internal/database"
	"github.com/cybertec-postgresql/pgcov/internal/testutil"
	"github.com/cybertec-postgresql/pgcov/pkg/types"
)

// TestNewPool_RejectsMalformedConnectionString covers the parse-failure branch
// without needing a server, and pins the guidance the error carries.
func TestNewPool_RejectsMalformedConnectionString(t *testing.T) {
	_, err := database.NewPool(context.Background(), &types.Config{
		ConnectionString: "://not-a-connection-string",
		Timeout:          time.Second,
		Parallelism:      1,
	})
	if err == nil {
		t.Fatal("a malformed connection string must be rejected")
	}
	var connErr *database.ConnectionError
	if !asConnectionError(err, &connErr) {
		t.Fatalf("expected a *ConnectionError, got %T", err)
	}
	if !strings.Contains(connErr.Suggestion, "URI format") {
		t.Errorf("suggestion %q should explain the accepted formats", connErr.Suggestion)
	}
}

func asConnectionError(err error, target **database.ConnectionError) bool {
	ce, ok := err.(*database.ConnectionError)
	if ok {
		*target = ce
	}
	return ok
}

func TestConnectionError_Error(t *testing.T) {
	e := &database.ConnectionError{
		Host: "db.example", Port: 5432,
		Message:    "connection refused",
		Suggestion: "start the server",
	}
	msg := e.Error()
	for _, want := range []string{"db.example", "5432", "connection refused", "start the server"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}

	// Without a suggestion the message must not trail an empty section.
	bare := (&database.ConnectionError{Message: "boom"}).Error()
	if strings.Contains(bare, "Suggestion") {
		t.Errorf("bare error should carry no suggestion section: %q", bare)
	}
}

// TestNewPool_MaxConnsTracksParallelism pins the sizing rule against a real
// server: sequential runs get a fixed small pool, parallel runs scale with the
// worker count.
func TestNewPool_MaxConnsTracksParallelism(t *testing.T) {
	connString, cleanup := testutil.SetupPostgresContainer(t)
	defer cleanup()

	cases := []struct {
		parallelism int
		wantMax     int32
	}{
		{parallelism: 1, wantMax: 4},
		{parallelism: 2, wantMax: 4},
		{parallelism: 8, wantMax: 16},
	}

	for _, tc := range cases {
		cfg := &types.Config{
			ConnectionString: connString,
			Timeout:          30 * time.Second,
			Parallelism:      tc.parallelism,
		}
		pool, err := database.NewPool(context.Background(), cfg)
		if err != nil {
			t.Fatalf("parallelism %d: %v", tc.parallelism, err)
		}
		if got := pool.Config().MaxConns; got != tc.wantMax {
			t.Errorf("parallelism %d: MaxConns = %d, want %d", tc.parallelism, got, tc.wantMax)
		}
		pool.Close()
	}
}

// TestNewPool_SetsApplicationName makes pgcov's connections identifiable in
// pg_stat_activity, which is how an operator finds a run that went wrong.
func TestNewPool_SetsApplicationName(t *testing.T) {
	connString, cleanup := testutil.SetupPostgresContainer(t)
	defer cleanup()

	ctx := context.Background()
	pool, err := database.NewPool(ctx, &types.Config{
		ConnectionString: connString,
		Timeout:          30 * time.Second,
		Parallelism:      1,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var appName string
	if err := pool.QueryRow(ctx, "SHOW application_name").Scan(&appName); err != nil {
		t.Fatalf("query: %v", err)
	}
	if appName != "pgcov" {
		t.Errorf("application_name = %q, want %q", appName, "pgcov")
	}
}

// TestNewPool_AcceptsSupportedServerVersion exercises the version gate's happy
// path; the container is well above the 13 minimum.
func TestNewPool_AcceptsSupportedServerVersion(t *testing.T) {
	connString, cleanup := testutil.SetupPostgresContainer(t)
	defer cleanup()

	pool, err := database.NewPool(context.Background(), &types.Config{
		ConnectionString: connString,
		Timeout:          30 * time.Second,
		Parallelism:      1,
	})
	if err != nil {
		t.Fatalf("a supported server version was rejected: %v", err)
	}
	pool.Close()
}
