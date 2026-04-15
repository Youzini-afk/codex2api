package admin

import (
	"database/sql"
	"testing"
)

func TestDatabaseUsagePercentUsesInUseConnections(t *testing.T) {
	stats := sql.DBStats{
		OpenConnections:    1,
		InUse:              0,
		Idle:               1,
		MaxOpenConnections: 1,
	}

	got := databaseUsagePercent(stats)
	if got != 0 {
		t.Fatalf("databaseUsagePercent() = %v, want 0", got)
	}
}

func TestDatabaseUsagePercentCapsAtMaxOpenConnections(t *testing.T) {
	stats := sql.DBStats{
		InUse:              7,
		MaxOpenConnections: 5,
	}

	got := databaseUsagePercent(stats)
	if got != 100 {
		t.Fatalf("databaseUsagePercent() = %v, want 100", got)
	}
}
