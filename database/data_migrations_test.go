package database

import (
	"context"
	"testing"
)

func TestCurrentDataMigrationsAreUniqueAndIncludeGroupChannel(t *testing.T) {
	db := &DB{}
	migrations := db.currentDataMigrations()
	if len(migrations) == 0 {
		t.Fatal("currentDataMigrations returned no migrations")
	}
	seen := make(map[string]struct{}, len(migrations))
	for _, migration := range migrations {
		if migration.version == "" || migration.migrate == nil {
			t.Fatalf("invalid migration spec: %#v", migration)
		}
		if _, duplicate := seen[migration.version]; duplicate {
			t.Fatalf("duplicate current data migration %q", migration.version)
		}
		seen[migration.version] = struct{}{}
	}
	if migrations[len(migrations)-1].version != dataMigrationGroupChannelV1 {
		t.Fatalf("last migration=%q, want %q", migrations[len(migrations)-1].version, dataMigrationGroupChannelV1)
	}
}

func TestGroupChannelDataMigrationClassifiesOnlyAllGrokGroupsAndWritesMarker(t *testing.T) {
	db, err := New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	for _, statement := range []string{
		`INSERT INTO accounts (id, name, credentials, status) VALUES
			(101, 'grok-active', '{"upstream_type":"grok"}', 'active'),
			(102, 'codex-active', '{}', 'active'),
			(103, 'codex-deleted', '{}', 'deleted')`,
		`INSERT INTO account_groups (id, name, channel) VALUES
			(201, 'all-grok', 'codex'),
			(202, 'mixed', 'codex'),
			(203, 'empty', 'codex'),
			(204, 'grok-with-deleted-codex', 'codex')`,
		`INSERT INTO account_group_members (account_id, group_id) VALUES
			(101, 201), (101, 202), (102, 202), (101, 204), (103, 204)`,
		`DELETE FROM data_migrations WHERE version='20260807_account_group_channel_v1'`,
	} {
		if _, err := db.conn.ExecContext(ctx, statement); err != nil {
			t.Fatalf("fixture statement failed: %v\n%s", err, statement)
		}
	}

	if err := db.runDataMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := db.conn.QueryContext(ctx, `SELECT id, channel FROM account_groups ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make(map[int64]string)
	for rows.Next() {
		var id int64
		var channel string
		if err := rows.Scan(&id, &channel); err != nil {
			t.Fatal(err)
		}
		got[id] = channel
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]string{
		201: AccountGroupChannelGrok,
		202: AccountGroupChannelCodex,
		203: AccountGroupChannelCodex,
		204: AccountGroupChannelGrok,
	} {
		if got[id] != want {
			t.Errorf("group %d channel=%q, want %q", id, got[id], want)
		}
	}
	var markers int
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM data_migrations WHERE version=$1`, dataMigrationGroupChannelV1).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 1 {
		t.Fatalf("group channel marker count=%d, want 1", markers)
	}
}
