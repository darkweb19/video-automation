package app

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"math"
	"testing"
	"time"
)

// These records are inserted directly so the inventory remains testable after
// public creation and dispatch of legacy jobs have been fenced off.
func legacyDrainAttemptFixture(t *testing.T, store *Store, name, provider, jobStatus, attemptStatus string, lease int64, issued *int64) {
	t.Helper()
	sourceID, batchID := "source_"+name, "batch_"+name
	jobID, attemptID := legacyDrainFixtureIDs(name)
	for _, item := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO clipping_sources(id,kind,status,storage_path,declared_size_bytes,reserved_size_bytes,retain_until,created_at,updated_at)
			VALUES(?,'upload','ready',?,1,1,1,1,1)`, []any{sourceID, "/fixture/" + name}},
		{`INSERT INTO clipping_batches(id,idempotency_key,request_hash,status,budget_limit_micro_usd,created_at,updated_at)
			VALUES(?,?,?,'queued',1,1,1)`, []any{batchID, "batch-key-" + name, "hash"}},
		{`INSERT INTO clipping_jobs(id,batch_id,source_id,provider,min_clip_seconds,max_clip_seconds,candidate_limit,status,budget_limit_micro_usd,created_at,updated_at)
			VALUES(?,?,?,?,?,?,7,?,1,1,1)`, []any{jobID, batchID, sourceID, provider, legacyDrainSeconds(provider, 15), legacyDrainSeconds(provider, 180), jobStatus}},
		{`INSERT INTO clipping_stages(job_id,name,status,idempotency_key,attempt_id,attempt,created_at,updated_at)
			VALUES(?,'analysis','running',?,?,1,1,1)`, []any{jobID, "stage-key-" + name, attemptID}},
		{`INSERT INTO clipping_stage_attempts(attempt_id,job_id,stage_name,attempt_no,idempotency_key,token_hash,status,lease_expires_at,reserved_micro_usd,started_at)
			VALUES(?,?,'analysis',1,?,'hash',?,?,0,1)`, []any{attemptID, jobID, "attempt-key-" + name, attemptStatus, lease}},
	} {
		if _, err := store.db.Exec(item.query, item.args...); err != nil {
			t.Fatalf("create %s fixture: %v", name, err)
		}
	}
	if issued != nil {
		if _, err := store.db.Exec(`INSERT INTO clipping_capability_expiry_index(job_id,attempt_id,expires_at) VALUES(?,?,?)`, jobID, attemptID, *issued); err != nil {
			t.Fatal(err)
		}
	}
}

func legacyDrainFixtureIDs(name string) (string, string) {
	digest := sha256.Sum256([]byte(name))
	return fmt.Sprintf("clipjob_%x", digest[:16]), fmt.Sprintf("clipatt_%x", digest[:16])
}

func legacyDrainSeconds(provider string, value int) any {
	if provider == "gemini" {
		return nil
	}
	return value
}

func TestLegacyClippingDrainCountsTerminalAndPreIndexCallbacks(t *testing.T) {
	const now int64 = 2_000_000
	grace := int64((24 * time.Hour).Seconds())
	open, elapsed := now+100, now
	cases := []struct {
		name        string
		job         string
		attempt     string
		lease       int64
		issued      *int64
		wantOpen    int64
		wantUnknown int64
	}{
		{"running_indexed", "running", "running", now + 10, &open, 1, 0},
		{"completed_indexed", "completed", "completed", now - grace - 10, &open, 1, 0},
		{"canceled_indexed", "canceled", "canceled", now - grace - 10, &open, 1, 0},
		{"failed_indexed", "failed", "failed", now - grace - 10, &open, 1, 0},
		{"settled_terminal", "canceled", "settled_canceled", now - grace - 10, &open, 1, 0},
		{"exact_expiry", "running", "running", now + 10, &elapsed, 0, 0},
		{"preindex_terminal", "failed", "failed", now - 1, nil, 1, 0},
		{"preindex_expired", "failed", "expired", now - grace, nil, 0, 0},
		{"preindex_zero_lease", "failed", "failed", 0, nil, 0, 1},
		{"preindex_negative_lease", "failed", "failed", -1, nil, 0, 1},
		{"preindex_overflow", "failed", "failed", math.MaxInt64, nil, 0, 1},
		{"index_overrides_invalid_lease", "failed", "failed", 0, &open, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			legacyDrainAttemptFixture(t, store, tc.name, "modal_legacy", tc.job, tc.attempt, tc.lease, tc.issued)
			if tc.attempt == "completed" || tc.attempt == "settled_canceled" {
				if _, err := store.db.Exec(`UPDATE clipping_stage_attempts SET callback_received_at=?,settled_at=?`, now-5, now-5); err != nil {
					t.Fatal(err)
				}
			}
			inventory, err := store.LegacyClippingDrainInventory(now)
			if err != nil {
				t.Fatal(err)
			}
			if inventory.ObservedAt != now || inventory.UnexpiredCallbackWindows != tc.wantOpen || inventory.UnknownCallbackExpiry != tc.wantUnknown {
				t.Fatalf("inventory=%+v; want open=%d unknown=%d", inventory, tc.wantOpen, tc.wantUnknown)
			}
		})
	}
}

func TestLegacyClippingDrainRejectsInvalidPersistedExpiry(t *testing.T) {
	store := newTestStore(t)
	legacyDrainAttemptFixture(t, store, "invalid-index", "modal_legacy", "failed", "failed", 1, nil)
	jobID, attemptID := legacyDrainFixtureIDs("invalid-index")
	// Exercise corrupted or externally migrated data despite the current schema
	// constraint: an invalid issued expiry must not fall back to an old lease.
	if _, err := store.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO clipping_capability_expiry_index(job_id,attempt_id,expires_at) VALUES(?,?,0)`, jobID, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
		t.Fatal(err)
	}
	inventory, err := store.LegacyClippingDrainInventory(2_000_000)
	if err != nil || inventory.UnknownCallbackExpiry != 1 || inventory.UnexpiredCallbackWindows != 0 {
		t.Fatalf("invalid expiry inventory=%+v error=%v", inventory, err)
	}
}

func TestLegacyClippingDrainCountsImportClaimsAndOrphanedCapabilities(t *testing.T) {
	const now int64 = 2_000_000
	store := newTestStore(t)
	issued := now + 100
	legacyDrainAttemptFixture(t, store, "legacy", "modal_legacy", "canceled", "canceled", now, &issued)
	legacyDrainAttemptFixture(t, store, "gemini", "gemini", "running", "running", now+100, nil)
	jobID, attemptID := legacyDrainFixtureIDs("legacy")
	for _, item := range []struct {
		name, status string
		lease        int64
	}{
		{"pending", "importing", 0},
		{"active", "importing", now + 1},
		{"deleted-active", "deleted", now + 1},
		{"expired", "failed", now},
	} {
		if _, err := store.db.Exec(`INSERT INTO clipping_sources(id,kind,status,storage_path,declared_size_bytes,reserved_size_bytes,import_lease_until,created_at,updated_at)
			VALUES(?,'youtube_original_file',?,?,1,1,?,1,1)`, item.name, item.status, "/fixture/"+item.name, item.lease); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO clipping_analysis_claims(source_id,source_sha256,pipeline_key,job_id,attempt_id,claimed_at) VALUES(?,?,?,?,?,1)`,
		"source_legacy", fmt.Sprintf("%064d", 1), "legacy", jobID, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO clipping_capability_expiry_index(job_id,attempt_id,expires_at) VALUES('missing_job','missing_attempt',?)`, issued); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"clipping_cap_" + jobID + "_" + attemptID + "_callback",
		"clipping_cap_" + jobID + "_" + attemptID + "_callback_expiry",
		"clipping_cap_wrong_job_" + attemptID + "_callback",
		"clipping_cap_missing_job_missing_attempt_callback_expiry",
		"unrelated_setting",
	} {
		if err := store.SetSetting(key, "encrypted-fixture-value"); err != nil {
			t.Fatal(err)
		}
	}
	var before, after int64
	if err := store.db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	inventory, err := store.LegacyClippingDrainInventory(now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("inventory mutated database: changes before=%d after=%d", before, after)
	}
	if inventory.PendingImports != 2 || inventory.ActiveImportLeases != 2 || inventory.UnexpiredCallbackWindows != 1 ||
		inventory.UnknownCallbackExpiry != 0 || inventory.UnresolvedClaims != 1 || inventory.InconsistentCapabilityRows != 3 {
		t.Fatalf("unexpected aggregate inventory: %+v", inventory)
	}
}

func TestLegacyClippingDrainFailsClosedOnStorageErrors(t *testing.T) {
	var absent *Store
	if _, err := absent.LegacyClippingDrainInventory(1); err == nil {
		t.Fatal("nil store returned a usable drain inventory")
	}
	store := newTestStore(t)
	if _, err := store.LegacyClippingDrainInventory(0); err == nil {
		t.Fatal("invalid observation time returned a usable drain inventory")
	}
	if _, err := store.db.Exec(`DROP TABLE clipping_capability_expiry_index`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LegacyClippingDrainInventory(1); err == nil || err == sql.ErrNoRows {
		t.Fatalf("missing index table returned usable inventory: %v", err)
	}
}
