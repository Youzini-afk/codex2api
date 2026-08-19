package database

import (
	"context"
	"database/sql"
	"testing"
)

func TestAccountProjectionsKeepUsageReserveAndCredentialIdentityFields(t *testing.T) {
	db, err := New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	id, err := db.InsertAccountWithUpstream(ctx, "projection", "xai", "grok", map[string]any{
		"upstream_type": "grok", "api_key": "fixture",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `UPDATE accounts SET
		usage_reserve_percent_5h=17, usage_reserve_percent_7d=29,
		credential_generation=7, credential_family_id='family-projection'
		WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}

	assertProjection := func(label string, row *AccountRow) {
		t.Helper()
		if row == nil || row.ID != id {
			t.Fatalf("%s row=%#v, want id=%d", label, row, id)
		}
		if row.UsageReservePercent5h != (sql.NullInt64{Int64: 17, Valid: true}) ||
			row.UsageReservePercent7d != (sql.NullInt64{Int64: 29, Valid: true}) {
			t.Fatalf("%s reserves=%v/%v", label, row.UsageReservePercent5h, row.UsageReservePercent7d)
		}
		if row.CredentialGeneration != 7 || row.CredentialFamilyID != "family-projection" {
			t.Fatalf("%s credential identity=%d/%q", label, row.CredentialGeneration, row.CredentialFamilyID)
		}
	}

	active, err := db.ListActive(ctx)
	if err != nil || len(active) != 1 {
		t.Fatalf("ListActive rows=%d err=%v", len(active), err)
	}
	assertProjection("ListActive", active[0])
	byID, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	assertProjection("GetAccountByID", byID)
	selected, err := db.ListActiveByIDs(ctx, []int64{id})
	if err != nil || len(selected) != 1 {
		t.Fatalf("ListActiveByIDs rows=%d err=%v", len(selected), err)
	}
	assertProjection("ListActiveByIDs", selected[0])

	if err := db.SoftDeleteAccount(ctx, id); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.ListDeleted(ctx)
	if err != nil || len(deleted) != 1 {
		t.Fatalf("ListDeleted rows=%d err=%v", len(deleted), err)
	}
	assertProjection("ListDeleted", deleted[0])
}

func TestUpdateAccountSchedulerMetadataKeepsReserveSignatureAndSQLiteWriteLock(t *testing.T) {
	db, err := New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	id, err := db.InsertAccount(ctx, "scheduler", "refresh", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateAccountSchedulerMetadata(ctx, id,
		OptionalNullInt64{}, OptionalNullInt64{},
		OptionalNullInt64{Set: true, Value: sql.NullInt64{Int64: 11, Valid: true}},
		OptionalNullInt64{Set: true, Value: sql.NullInt64{Int64: 23, Valid: true}},
		OptionalBool{}, OptionalInt64Slice{}, OptionalStringSlice{}, OptionalInt64Slice{}, OptionalString{}, nil,
	); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !row.UsageReservePercent5h.Valid || row.UsageReservePercent5h.Int64 != 11 ||
		!row.UsageReservePercent7d.Valid || row.UsageReservePercent7d.Int64 != 23 {
		t.Fatalf("updated reserves=%v/%v", row.UsageReservePercent5h, row.UsageReservePercent7d)
	}
}
