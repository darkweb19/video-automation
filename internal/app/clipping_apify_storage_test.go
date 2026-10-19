package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const clippingApifyTestActorID = "sample-user~youtube-video-downloader"

func createClaimedClippingApifySource(t *testing.T, store *Store) ClippingSource {
	t.Helper()
	source, err := store.CreateClippingSource(ClippingSourceCreate{
		Kind: ClippingSourceYouTube, SourceURL: "https://www.youtube.com/watch?v=abcdefghijk",
		DeclaredSizeBytes: 1 << 20, RightsAttested: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.BeginClippingImport(source.ID, 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return claimed
}

func retryAndClaimClippingApifySource(t *testing.T, store *Store, source ClippingSource) ClippingSource {
	t.Helper()
	nextAt := time.Now().Add(-time.Second).Unix()
	if err := store.RetryClippingImport(source.ID, source.ImportAttempts, nextAt, "retry import"); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.BeginClippingImport(source.ID, source.ImportAttempts, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return claimed
}

func TestClippingApifyDuplicateStartCASAndRunIDBinding(t *testing.T) {
	store := newTestStore(t)
	source := createClaimedClippingApifySource(t, store)
	first, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, clippingApifyTestActorID, 2*time.Hour)
	if err != nil || !first.MayStart {
		t.Fatalf("initial durable intent: mayStart=%v state=%+v error=%v", first.MayStart, first.State, err)
	}
	if first.State.StartState != clippingApifyStartIntent || first.State.StartIntentAt <= 0 || first.State.DeadlineAt <= first.State.StartIntentAt {
		t.Fatalf("initial intent lacks timestamp/deadline: %+v", first.State)
	}
	duplicate, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, clippingApifyTestActorID, time.Hour)
	if err != nil || duplicate.MayStart || duplicate.State.StartIntentAt != first.State.StartIntentAt {
		t.Fatalf("duplicate prepare was not read-only: claim=%+v error=%v", duplicate, err)
	}

	started, mayDispatch, err := store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts, clippingApifyTestActorID)
	if err != nil || !mayDispatch || started.StartState != clippingApifyStartUnknown {
		t.Fatalf("initial start CAS: mayDispatch=%v state=%+v error=%v", mayDispatch, started, err)
	}
	duplicateStart, mayDispatch, err := store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts, clippingApifyTestActorID)
	if err != nil || mayDispatch || duplicateStart.StartState != clippingApifyStartUnknown {
		t.Fatalf("duplicate start CAS permitted a second request: mayDispatch=%v state=%+v error=%v", mayDispatch, duplicateStart, err)
	}

	attached, err := store.AttachClippingApifyRunID(source.ID, source.ImportAttempts, clippingApifyTestActorID, "run_123-abc")
	if err != nil || attached.RunID != "run_123-abc" || attached.StartState != clippingApifyRunAttached {
		t.Fatalf("attach actor run: state=%+v error=%v", attached, err)
	}
	replay, err := store.AttachClippingApifyRunID(source.ID, source.ImportAttempts, clippingApifyTestActorID, "run_123-abc")
	if err != nil || replay.RunID != attached.RunID {
		t.Fatalf("same run ID replay should be idempotent: state=%+v error=%v", replay, err)
	}
	if _, err := store.AttachClippingApifyRunID(source.ID, source.ImportAttempts, clippingApifyTestActorID, "different_run"); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("different run ID replaced the once-bound value: %v", err)
	}
}

func TestClippingApifyAttemptLeaseAndActorFencing(t *testing.T) {
	store := newTestStore(t)
	source := createClaimedClippingApifySource(t, store)
	if _, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, clippingApifyTestActorID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, mayDispatch, err := store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts+1, clippingApifyTestActorID); !errors.Is(err, ErrClippingInvalidState) || mayDispatch {
		t.Fatalf("stale attempt marked a start: mayDispatch=%v error=%v", mayDispatch, err)
	}
	if _, mayDispatch, err := store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts, "other-user~other-actor"); !errors.Is(err, ErrClippingInvalidState) || mayDispatch {
		t.Fatalf("actor mismatch marked a start: mayDispatch=%v error=%v", mayDispatch, err)
	}
	if err := store.RenewClippingImportLease(source.ID, source.ImportAttempts, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, mayDispatch, err := store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts, clippingApifyTestActorID); err != nil || !mayDispatch {
		t.Fatalf("live matching lease could not mark the start: mayDispatch=%v error=%v", mayDispatch, err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET import_lease_until=?,next_import_at=? WHERE id=?`, time.Now().Unix()-1, time.Now().Unix()-1, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AttachClippingApifyRunID(source.ID, source.ImportAttempts, clippingApifyTestActorID, "run_after_lease"); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("expired lease bound an actor run: %v", err)
	}

	// An ambiguous start survives retries without dispatch permission.
	second := retryAndClaimClippingApifySource(t, store, source)
	claim, err := store.PrepareClippingApifyRun(second.ID, second.ImportAttempts, clippingApifyTestActorID, time.Hour)
	if err != nil || claim.MayStart || claim.State.ImportAttempt != source.ImportAttempts || claim.State.StartState != clippingApifyStartUnknown {
		t.Fatalf("ambiguous start incorrectly permitted a new paid run: claim=%+v error=%v", claim, err)
	}
	if _, mayDispatch, err := store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts, clippingApifyTestActorID); !errors.Is(err, ErrClippingInvalidState) || mayDispatch {
		t.Fatalf("stale worker started with an expired attempt: mayDispatch=%v error=%v", mayDispatch, err)
	}
}

func TestClippingApifyUnstartedIntentRollsForwardToNewAttempt(t *testing.T) {
	store := newTestStore(t)
	source := createClaimedClippingApifySource(t, store)
	first, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, clippingApifyTestActorID, time.Hour)
	if err != nil || !first.MayStart || first.State.StartState != clippingApifyStartIntent {
		t.Fatalf("prepare intent before any start request: claim=%+v error=%v", first, err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET import_lease_until=?,next_import_at=? WHERE id=?`, time.Now().Unix()-1, time.Now().Unix()-1, source.ID); err != nil {
		t.Fatal(err)
	}
	second := retryAndClaimClippingApifySource(t, store, source)
	claim, err := store.PrepareClippingApifyRun(second.ID, second.ImportAttempts, clippingApifyTestActorID, time.Hour)
	if err != nil || !claim.MayStart || claim.State.ImportAttempt != second.ImportAttempts || claim.State.StartState != clippingApifyStartIntent {
		t.Fatalf("never-started intent did not roll forward under a new lease: claim=%+v error=%v", claim, err)
	}
	if _, mayDispatch, err := store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts, clippingApifyTestActorID); !errors.Is(err, ErrClippingInvalidState) || mayDispatch {
		t.Fatalf("stale attempt crossed the start boundary: mayDispatch=%v error=%v", mayDispatch, err)
	}
}

func TestClippingApifyDeletedOrCanceledSourceCannotStartOrBind(t *testing.T) {
	t.Run("deleted", func(t *testing.T) {
		store := newTestStore(t)
		source := createClaimedClippingApifySource(t, store)
		if _, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, clippingApifyTestActorID, time.Hour); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DeleteClippingSource(source.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, clippingApifyTestActorID, time.Hour); !errors.Is(err, ErrClippingInvalidState) {
			t.Fatalf("deleted source allowed a new start: %v", err)
		}
		if _, mayDispatch, err := store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts, clippingApifyTestActorID); !errors.Is(err, ErrClippingInvalidState) || mayDispatch {
			t.Fatalf("deleted source crossed the start boundary: mayDispatch=%v error=%v", mayDispatch, err)
		}
		if _, err := store.AttachClippingApifyRunID(source.ID, source.ImportAttempts, clippingApifyTestActorID, "late_run"); !errors.Is(err, ErrClippingInvalidState) {
			t.Fatalf("deleted source bound a late actor response: %v", err)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		store := newTestStore(t)
		source := createClaimedClippingApifySource(t, store)
		if _, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, clippingApifyTestActorID, time.Hour); err != nil {
			t.Fatal(err)
		}
		if err := store.FailClippingSource(source.ID, "source_import_canceled"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, clippingApifyTestActorID, time.Hour); !errors.Is(err, ErrClippingInvalidState) {
			t.Fatalf("canceled source allowed a new start: %v", err)
		}
		if _, mayDispatch, err := store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts, clippingApifyTestActorID); !errors.Is(err, ErrClippingInvalidState) || mayDispatch {
			t.Fatalf("canceled source crossed the start boundary: mayDispatch=%v error=%v", mayDispatch, err)
		}
		if _, err := store.AttachClippingApifyRunID(source.ID, source.ImportAttempts, clippingApifyTestActorID, "late_run"); !errors.Is(err, ErrClippingInvalidState) {
			t.Fatalf("canceled source bound a late actor response: %v", err)
		}
	})
}

func TestClippingApifyDefinitiveRejectionAllowsOnlyNextAttemptRetry(t *testing.T) {
	store := newTestStore(t)
	source := createClaimedClippingApifySource(t, store)
	claim, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, clippingApifyTestActorID, time.Hour)
	if err != nil || !claim.MayStart {
		t.Fatalf("prepare initial request: claim=%+v error=%v", claim, err)
	}
	if _, mayDispatch, err := store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts, clippingApifyTestActorID); err != nil || !mayDispatch {
		t.Fatalf("mark before first request: mayDispatch=%v error=%v", mayDispatch, err)
	}
	if _, err := store.ResolveClippingApifyStartRejected(source.ID, source.ImportAttempts, clippingApifyTestActorID); err != nil {
		t.Fatalf("resolve documented definitive rejection: %v", err)
	}
	if claim, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, clippingApifyTestActorID, time.Hour); err != nil || claim.MayStart {
		t.Fatalf("rejection permitted a second start within the same attempt: claim=%+v error=%v", claim, err)
	}
	second := retryAndClaimClippingApifySource(t, store, source)
	claim, err = store.PrepareClippingApifyRun(second.ID, second.ImportAttempts, clippingApifyTestActorID, time.Hour)
	if err != nil || !claim.MayStart || claim.State.ImportAttempt != second.ImportAttempts || claim.State.StartState != clippingApifyStartIntent {
		t.Fatalf("definitive rejection did not permit one later attempt: claim=%+v error=%v", claim, err)
	}
	if _, mayDispatch, err := store.MarkClippingApifyStartUnknown(second.ID, second.ImportAttempts, clippingApifyTestActorID); err != nil || !mayDispatch {
		t.Fatalf("later attempt could not claim the start CAS: mayDispatch=%v error=%v", mayDispatch, err)
	}
	if _, err := store.PrepareClippingApifyRun(second.ID, second.ImportAttempts, "changed-user~actor", time.Hour); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("actor snapshot was mutable across retries: %v", err)
	}
}

func TestClippingApifyRunCorrelationSurvivesExpectationResetAndReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	source := createClaimedClippingApifySource(t, store)
	if _, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, clippingApifyTestActorID, 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, mayDispatch, err := store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts, clippingApifyTestActorID); err != nil || !mayDispatch {
		t.Fatalf("mark durable start: mayDispatch=%v error=%v", mayDispatch, err)
	}
	if _, err := store.AttachClippingApifyRunID(source.ID, source.ImportAttempts, clippingApifyTestActorID, "run_persisted_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET import_expectation_json=? WHERE id=?`, `{"old":"transient expectation"}`, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetClippingSourceImportProgress(source.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, found, err := reopened.GetClippingApifyRunState(source.ID)
	if err != nil || !found || state.RunID != "run_persisted_1" || state.ActorID != clippingApifyTestActorID || state.StartState != clippingApifyRunAttached {
		t.Fatalf("durable Apify run state after reopen: found=%v state=%+v error=%v", found, state, err)
	}
	loaded, err := reopened.ClippingSource(source.ID)
	if err != nil || loaded.ImportExpectation != "" {
		t.Fatalf("source expectation reset was not preserved independently: expectation=%q error=%v", loaded.ImportExpectation, err)
	}
	claimed := retryAndClaimClippingApifySource(t, reopened, loaded)
	claim, err := reopened.PrepareClippingApifyRun(claimed.ID, claimed.ImportAttempts, clippingApifyTestActorID, time.Hour)
	if err != nil || claim.MayStart || claim.State.RunID != "run_persisted_1" || claim.State.ActorID != clippingApifyTestActorID {
		t.Fatalf("retry attempted a new paid start instead of recovering the attached run: claim=%+v error=%v", claim, err)
	}
}

func TestClippingApifyAmbiguousStartSurvivesRestartAndNeverResubmits(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	source := createClaimedClippingApifySource(t, store)
	if _, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, clippingApifyTestActorID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, mayDispatch, err := store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts, clippingApifyTestActorID); err != nil || !mayDispatch {
		t.Fatalf("mark pre-POST ambiguity boundary: mayDispatch=%v error=%v", mayDispatch, err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET import_lease_until=?,next_import_at=? WHERE id=?`, time.Now().Unix()-1, time.Now().Unix()-1, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, found, err := reopened.GetClippingApifyRunState(source.ID)
	if err != nil || !found || state.StartState != clippingApifyStartUnknown || state.RunID != "" {
		t.Fatalf("ambiguous state was lost on restart: found=%v state=%+v error=%v", found, state, err)
	}
	loaded, err := reopened.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed := retryAndClaimClippingApifySource(t, reopened, loaded)
	claim, err := reopened.PrepareClippingApifyRun(claimed.ID, claimed.ImportAttempts, clippingApifyTestActorID, time.Hour)
	if err != nil || claim.MayStart || claim.State.StartState != clippingApifyStartUnknown || claim.State.RunID != "" {
		t.Fatalf("restart/retry repeated an ambiguous paid start: claim=%+v error=%v", claim, err)
	}
}

func TestClippingApifyMigrationPreservesLegacySourceRows(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.CreateClippingSource(ClippingSourceCreate{
		Kind: ClippingSourceYouTube, SourceURL: "https://www.youtube.com/watch?v=abcdefghijk",
		DeclaredSizeBytes: 1 << 20, RightsAttested: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyExpectation := `{"legacy":"keep during additive migration"}`
	if _, err := store.db.Exec(`UPDATE clipping_sources SET import_expectation_json=? WHERE id=?`, legacyExpectation, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE clipping_apify_runs`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.ClippingSource(source.ID)
	if err != nil || loaded.SourceURL != source.SourceURL || loaded.ImportExpectation != legacyExpectation || loaded.Status != ClippingSourceImporting {
		t.Fatalf("legacy source changed during additive migration: source=%+v error=%v", loaded, err)
	}
	state, found, err := reopened.GetClippingApifyRunState(source.ID)
	if err != nil || found {
		t.Fatalf("legacy source unexpectedly acquired Apify run state: found=%v state=%+v error=%v", found, state, err)
	}
	claimed, err := reopened.BeginClippingImport(source.ID, 0, time.Hour)
	if err != nil {
		t.Fatalf("legacy import did not retain its existing queue claimability: %v", err)
	}
	if _, err := reopened.PrepareClippingApifyRun(claimed.ID, claimed.ImportAttempts, clippingApifyTestActorID, time.Hour); err != nil {
		t.Fatalf("new additive state could not be recorded for a legacy source: %v", err)
	}
}

func TestClippingApifyCorrelationStaysOutOfPublicSourceAndHasNoCredentialColumns(t *testing.T) {
	store := newTestStore(t)
	source := createClaimedClippingApifySource(t, store)
	if _, err := store.PrepareClippingApifyRun(source.ID, source.ImportAttempts, clippingApifyTestActorID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, mayDispatch, err := store.MarkClippingApifyStartUnknown(source.ID, source.ImportAttempts, clippingApifyTestActorID); err != nil || !mayDispatch {
		t.Fatalf("mark actor start: mayDispatch=%v error=%v", mayDispatch, err)
	}
	if _, err := store.AttachClippingApifyRunID(source.ID, source.ImportAttempts, clippingApifyTestActorID, "run_private_marker_7"); err != nil {
		t.Fatal(err)
	}
	publicSource, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(publicSource)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), clippingApifyTestActorID) || strings.Contains(string(encoded), "run_private_marker_7") {
		t.Fatalf("internal Apify correlation leaked through the source API model: %s", encoded)
	}
	rows, err := store.db.Query(`PRAGMA table_info(clipping_apify_runs)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		lowerName := strings.ToLower(name)
		if strings.Contains(lowerName, "token") || strings.Contains(lowerName, "url") || strings.Contains(lowerName, "secret") {
			t.Fatalf("Apify correlation schema contains credential or URL column %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func setClippingApifyFinalExpectation(t *testing.T, store *Store, source ClippingSource) (int64, string) {
	t.Helper()
	expectation := youtubeImportExpectation{Version: 2, SeparateTracks: true, WorkspaceComplete: true}
	if err := store.SetClippingSourceImportExpectation(source.ID, expectation); err != nil {
		t.Fatal(err)
	}
	const sizeBytes = 100
	const sha256Hex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := store.SetClippingSourceImportFinalExpectation(source.ID, sizeBytes, sha256Hex); err != nil {
		t.Fatal(err)
	}
	return sizeBytes, sha256Hex
}

func TestClippingApifyReadyPublicationRequiresCurrentLiveImportAttempt(t *testing.T) {
	t.Run("current attempt publishes", func(t *testing.T) {
		store := newTestStore(t)
		source := createClaimedClippingApifySource(t, store)
		sizeBytes, sha256Hex := setClippingApifyFinalExpectation(t, store, source)
		if err := store.MarkClippingSourceImportReady(source.ID, source.ImportAttempts, sizeBytes, 30_000, sha256Hex); err != nil {
			t.Fatalf("active import failed to publish ready: %v", err)
		}
		loaded, err := store.ClippingSource(source.ID)
		if err != nil || loaded.Status != ClippingSourceReady {
			t.Fatalf("source did not become ready: status=%s error=%v", loaded.Status, err)
		}
	})

	t.Run("wrong attempt rejected", func(t *testing.T) {
		store := newTestStore(t)
		source := createClaimedClippingApifySource(t, store)
		sizeBytes, sha256Hex := setClippingApifyFinalExpectation(t, store, source)
		if err := store.MarkClippingSourceImportReady(source.ID, source.ImportAttempts+1, sizeBytes, 30_000, sha256Hex); !errors.Is(err, ErrClippingInvalidState) {
			t.Fatalf("wrong attempt published source: %v", err)
		}
	})

	t.Run("expired lease rejected", func(t *testing.T) {
		store := newTestStore(t)
		source := createClaimedClippingApifySource(t, store)
		sizeBytes, sha256Hex := setClippingApifyFinalExpectation(t, store, source)
		if _, err := store.db.Exec(`UPDATE clipping_sources SET import_lease_until=?,next_import_at=? WHERE id=?`, time.Now().Unix()-1, time.Now().Unix()-1, source.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkClippingSourceImportReady(source.ID, source.ImportAttempts, sizeBytes, 30_000, sha256Hex); !errors.Is(err, ErrClippingInvalidState) {
			t.Fatalf("expired attempt published source: %v", err)
		}
	})

	t.Run("deleted source rejected", func(t *testing.T) {
		store := newTestStore(t)
		source := createClaimedClippingApifySource(t, store)
		sizeBytes, sha256Hex := setClippingApifyFinalExpectation(t, store, source)
		if _, err := store.DeleteClippingSource(source.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkClippingSourceImportReady(source.ID, source.ImportAttempts, sizeBytes, 30_000, sha256Hex); !errors.Is(err, ErrClippingInvalidState) {
			t.Fatalf("deleted source became ready: %v", err)
		}
	})

	t.Run("canceled source rejected", func(t *testing.T) {
		store := newTestStore(t)
		source := createClaimedClippingApifySource(t, store)
		sizeBytes, sha256Hex := setClippingApifyFinalExpectation(t, store, source)
		if err := store.FailClippingSource(source.ID, "source_import_canceled"); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkClippingSourceImportReady(source.ID, source.ImportAttempts, sizeBytes, 30_000, sha256Hex); !errors.Is(err, ErrClippingInvalidState) {
			t.Fatalf("canceled source became ready: %v", err)
		}
	})
}

func TestClippingImportProgressAndFinalExpectationAreAttemptAndLeaseFenced(t *testing.T) {
	store := newTestStore(t)
	source := createClaimedClippingApifySource(t, store)
	if err := store.SetClippingSourceImportExpectation(source.ID, youtubeImportExpectation{
		Version: clippingYouTubeExpectationVersion, SeparateTracks: false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateClippingSourceImportProgress(source.ID, source.ImportAttempts, 128); err != nil {
		t.Fatalf("persist progress for active attempt: %v", err)
	}
	if err := store.UpdateClippingSourceImportProgress(source.ID, source.ImportAttempts, 127); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("regressing progress was accepted: %v", err)
	}
	sha := strings.Repeat("a", 64)
	if err := store.SetClippingSourceImportFinalExpectationForAttempt(source.ID, source.ImportAttempts, 128, sha); err != nil {
		t.Fatalf("persist final expectation for active attempt: %v", err)
	}
	if err := store.UpdateClippingSourceImportProgress(source.ID, source.ImportAttempts+1, 128); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("stale attempt updated progress: %v", err)
	}
	if err := store.SetClippingSourceImportFinalExpectationForAttempt(source.ID, source.ImportAttempts+1, 128, sha); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("stale attempt changed final expectation: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE clipping_sources SET import_lease_until=? WHERE id=?`, time.Now().Unix()-1, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateClippingSourceImportProgress(source.ID, source.ImportAttempts, 129); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("expired lease updated progress: %v", err)
	}
	if err := store.SetClippingSourceImportFinalExpectationForAttempt(source.ID, source.ImportAttempts, 128, sha); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("expired lease changed final expectation: %v", err)
	}
}

func TestClippingApifyV3WorkspaceMarkerCompletesBeforeReady(t *testing.T) {
	t.Run("v3 marker completes and publishes", func(t *testing.T) {
		store := newTestStore(t)
		source := createClaimedClippingApifySource(t, store)
		expectation := youtubeImportExpectation{Version: clippingYouTubeExpectationVersion}
		if expectation.Version < 3 {
			t.Fatalf("test requires the Apify expectation version, got %d", expectation.Version)
		}
		if err := store.SetClippingSourceImportExpectation(source.ID, expectation); err != nil {
			t.Fatalf("store Apify expectation: %v", err)
		}
		if err := store.CompleteClippingSourceTrackWorkspace(source.ID); err != nil {
			t.Fatalf("persist completed Apify artifact marker: %v", err)
		}
		loaded, err := store.ClippingSource(source.ID)
		if err != nil {
			t.Fatal(err)
		}
		var completed youtubeImportExpectation
		if err := json.Unmarshal([]byte(loaded.ImportExpectation), &completed); err != nil || !completed.WorkspaceComplete || completed.SeparateTracks {
			t.Fatalf("v3 completion marker was not durable: expectation=%+v error=%v", completed, err)
		}
		const sizeBytes = 100
		const sha256Hex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if err := store.SetClippingSourceImportFinalExpectation(source.ID, sizeBytes, sha256Hex); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkClippingSourceImportReady(source.ID, source.ImportAttempts, sizeBytes, 30_000, sha256Hex); err != nil {
			t.Fatalf("completed Apify artifact could not become ready: %v", err)
		}
	})

	t.Run("v3 incomplete marker blocks ready", func(t *testing.T) {
		store := newTestStore(t)
		source := createClaimedClippingApifySource(t, store)
		expectation := youtubeImportExpectation{Version: clippingYouTubeExpectationVersion}
		if expectation.Version < 3 {
			t.Fatalf("test requires the Apify expectation version, got %d", expectation.Version)
		}
		if err := store.SetClippingSourceImportExpectation(source.ID, expectation); err != nil {
			t.Fatalf("store Apify expectation: %v", err)
		}
		const sizeBytes = 100
		const sha256Hex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if err := store.SetClippingSourceImportFinalExpectation(source.ID, sizeBytes, sha256Hex); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkClippingSourceImportReady(source.ID, source.ImportAttempts, sizeBytes, 30_000, sha256Hex); !errors.Is(err, ErrClippingInvalidState) {
			t.Fatalf("incomplete Apify artifact was marked ready: %v", err)
		}
	})
}
