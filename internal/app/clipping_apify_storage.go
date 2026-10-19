package app

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const maxClippingApifyPollDuration = 24 * time.Hour

type clippingApifyStartState string

const (
	clippingApifyStartIntent   clippingApifyStartState = "intent"
	clippingApifyStartUnknown  clippingApifyStartState = "start_unknown"
	clippingApifyRunAttached   clippingApifyStartState = "run_attached"
	clippingApifyStartRejected clippingApifyStartState = "start_rejected"
)

// clippingApifyRunState is private durable correlation data. It is deliberately
// separate from ClippingSource and must never be serialized into source APIs.
// It contains no Apify token or temporary media URL.
type clippingApifyRunState struct {
	SourceID      string
	ActorID       string
	ImportAttempt int
	StartIntentAt int64
	DeadlineAt    int64
	RunID         string
	StartState    clippingApifyStartState
}

type clippingApifyStartClaim struct {
	State    clippingApifyRunState
	MayStart bool
}

var clippingApifyActorIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~/-]{1,256}$`)
var clippingApifyRunIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (s *Store) migrateClippingApifyStorage() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS clipping_apify_runs (
			source_id TEXT PRIMARY KEY REFERENCES clipping_sources(id) ON DELETE RESTRICT,
			actor_id TEXT NOT NULL CHECK(length(actor_id) BETWEEN 1 AND 256),
			import_attempt INTEGER NOT NULL CHECK(import_attempt > 0),
			start_intent_at INTEGER NOT NULL CHECK(start_intent_at > 0),
			deadline_at INTEGER NOT NULL CHECK(deadline_at > start_intent_at),
			run_id TEXT NOT NULL DEFAULT '',
			start_state TEXT NOT NULL CHECK(start_state IN ('intent','start_unknown','run_attached','start_rejected')),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			CHECK((start_state='run_attached' AND run_id<>'') OR (start_state<>'run_attached' AND run_id=''))
		);
	`)
	if err != nil {
		return fmt.Errorf("initialize clipping Apify run storage: %w", err)
	}
	return nil
}

// PrepareClippingApifyRun records the per-source actor snapshot, start intent,
// and bounded poll deadline before any external run-start request. MayStart is
// true only for a newly recorded intent or a safe new attempt after a prior
// intent never crossed the start boundary or was definitively rejected.
func (s *Store) PrepareClippingApifyRun(sourceID string, expectedAttempts int, actorID string, maxPollDuration time.Duration) (clippingApifyStartClaim, error) {
	if !safeClippingSourceID(sourceID) || expectedAttempts < 1 || expectedAttempts > MaxClippingImportAttempts ||
		!validClippingApifyActorID(actorID) || maxPollDuration <= 0 || maxPollDuration > maxClippingApifyPollDuration {
		return clippingApifyStartClaim{}, ErrClippingInvalidState
	}

	now := time.Now()
	nowUnix := now.Unix()
	deadlineAt := now.Add(maxPollDuration).Unix()
	if deadlineAt <= nowUnix {
		deadlineAt = nowUnix + 1
	}
	tx, err := s.db.Begin()
	if err != nil {
		return clippingApifyStartClaim{}, err
	}
	defer tx.Rollback()
	if err := validateClippingApifySourceLease(tx, sourceID, expectedAttempts, nowUnix); err != nil {
		return clippingApifyStartClaim{}, err
	}

	state, found, err := scanClippingApifyRunState(tx.QueryRow(`SELECT source_id,actor_id,import_attempt,start_intent_at,deadline_at,run_id,start_state
		FROM clipping_apify_runs WHERE source_id=?`, sourceID))
	if err != nil {
		return clippingApifyStartClaim{}, err
	}
	if !found {
		result, err := tx.Exec(`INSERT INTO clipping_apify_runs(source_id,actor_id,import_attempt,start_intent_at,deadline_at,run_id,start_state,created_at,updated_at)
			SELECT ?,?,?,?,?,'','intent',?,? WHERE EXISTS(SELECT 1 FROM clipping_sources WHERE id=? AND kind='youtube_original_file'
				AND status='importing' AND rights_attested=1 AND import_attempts=? AND import_lease_until>?)`,
			sourceID, actorID, expectedAttempts, nowUnix, deadlineAt, nowUnix, nowUnix, sourceID, expectedAttempts, nowUnix)
		if err != nil {
			return clippingApifyStartClaim{}, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return clippingApifyStartClaim{}, err
		}
		if changed != 1 {
			return clippingApifyStartClaim{}, ErrClippingInvalidState
		}
		state = clippingApifyRunState{
			SourceID: sourceID, ActorID: actorID, ImportAttempt: expectedAttempts,
			StartIntentAt: nowUnix, DeadlineAt: deadlineAt, StartState: clippingApifyStartIntent,
		}
		if err := tx.Commit(); err != nil {
			return clippingApifyStartClaim{}, err
		}
		return clippingApifyStartClaim{State: state, MayStart: true}, nil
	}
	if state.ActorID != actorID {
		return clippingApifyStartClaim{}, ErrClippingInvalidState
	}
	if state.ImportAttempt > expectedAttempts {
		return clippingApifyStartClaim{}, ErrClippingInvalidState
	}
	if state.StartState == clippingApifyStartUnknown || state.StartState == clippingApifyRunAttached {
		if err := tx.Commit(); err != nil {
			return clippingApifyStartClaim{}, err
		}
		return clippingApifyStartClaim{State: state}, nil
	}
	if state.StartState != clippingApifyStartIntent && state.StartState != clippingApifyStartRejected {
		return clippingApifyStartClaim{}, ErrClippingInvalidState
	}
	if state.ImportAttempt == expectedAttempts {
		if err := tx.Commit(); err != nil {
			return clippingApifyStartClaim{}, err
		}
		return clippingApifyStartClaim{State: state}, nil
	}

	// An unmarked intent cannot have reached Apify because every outbound start
	// is preceded by the intent->start_unknown CAS. A definitive rejection is
	// also safe to retry. Roll either case forward only under the new live lease.
	result, err := tx.Exec(`UPDATE clipping_apify_runs SET import_attempt=?,start_intent_at=?,deadline_at=?,start_state='intent',updated_at=?
		WHERE source_id=? AND import_attempt=? AND run_id='' AND start_state IN ('intent','start_rejected')
		AND EXISTS(SELECT 1 FROM clipping_sources WHERE id=? AND kind='youtube_original_file' AND status='importing'
			AND rights_attested=1 AND import_attempts=? AND import_lease_until>?)`,
		expectedAttempts, nowUnix, deadlineAt, nowUnix, sourceID, state.ImportAttempt, sourceID, expectedAttempts, nowUnix)
	if err != nil {
		return clippingApifyStartClaim{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return clippingApifyStartClaim{}, err
	}
	if changed != 1 {
		return clippingApifyStartClaim{}, ErrClippingInvalidState
	}
	state.ImportAttempt = expectedAttempts
	state.StartIntentAt = nowUnix
	state.DeadlineAt = deadlineAt
	state.StartState = clippingApifyStartIntent
	if err := tx.Commit(); err != nil {
		return clippingApifyStartClaim{}, err
	}
	return clippingApifyStartClaim{State: state, MayStart: true}, nil
}

// MarkClippingApifyStartUnknown is the durable boundary immediately before
// dispatching the actor-start request. Only the caller that wins this CAS may
// send that request. A persisted unknown outcome is never a retry permission.
func (s *Store) MarkClippingApifyStartUnknown(sourceID string, expectedAttempts int, actorID string) (clippingApifyRunState, bool, error) {
	if !safeClippingSourceID(sourceID) || expectedAttempts < 1 || expectedAttempts > MaxClippingImportAttempts || !validClippingApifyActorID(actorID) {
		return clippingApifyRunState{}, false, ErrClippingInvalidState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return clippingApifyRunState{}, false, err
	}
	defer tx.Rollback()
	state, err := clippingApifyAuthorizedState(tx, sourceID, expectedAttempts, actorID, time.Now().Unix())
	if err != nil {
		return clippingApifyRunState{}, false, err
	}
	if state.StartState != clippingApifyStartIntent {
		if err := tx.Commit(); err != nil {
			return clippingApifyRunState{}, false, err
		}
		return state, false, nil
	}
	now := time.Now().Unix()
	result, err := tx.Exec(`UPDATE clipping_apify_runs SET start_state='start_unknown',updated_at=?
		WHERE source_id=? AND actor_id=? AND import_attempt=? AND start_state='intent' AND run_id=''
		AND EXISTS(SELECT 1 FROM clipping_sources WHERE id=? AND kind='youtube_original_file' AND status='importing'
			AND rights_attested=1 AND import_attempts=? AND import_lease_until>?)`,
		now, sourceID, actorID, expectedAttempts, sourceID, expectedAttempts, now)
	if err != nil {
		return clippingApifyRunState{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return clippingApifyRunState{}, false, err
	}
	if changed != 1 {
		return clippingApifyRunState{}, false, ErrClippingInvalidState
	}
	state.StartState = clippingApifyStartUnknown
	if err := tx.Commit(); err != nil {
		return clippingApifyRunState{}, false, err
	}
	return state, true, nil
}

// AttachClippingApifyRunID binds the actor's acknowledged run ID exactly once.
// It requires the same live import attempt and actor snapshot that started it.
func (s *Store) AttachClippingApifyRunID(sourceID string, expectedAttempts int, actorID, runID string) (clippingApifyRunState, error) {
	if !safeClippingSourceID(sourceID) || expectedAttempts < 1 || expectedAttempts > MaxClippingImportAttempts ||
		!validClippingApifyActorID(actorID) || !clippingApifyRunIDPattern.MatchString(runID) {
		return clippingApifyRunState{}, ErrClippingInvalidState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return clippingApifyRunState{}, err
	}
	defer tx.Rollback()
	state, err := clippingApifyAuthorizedState(tx, sourceID, expectedAttempts, actorID, time.Now().Unix())
	if err != nil {
		return clippingApifyRunState{}, err
	}
	if state.StartState == clippingApifyRunAttached {
		if state.RunID != runID {
			return clippingApifyRunState{}, ErrClippingInvalidState
		}
		if err := tx.Commit(); err != nil {
			return clippingApifyRunState{}, err
		}
		return state, nil
	}
	if state.StartState != clippingApifyStartUnknown || state.RunID != "" {
		return clippingApifyRunState{}, ErrClippingInvalidState
	}
	now := time.Now().Unix()
	result, err := tx.Exec(`UPDATE clipping_apify_runs SET run_id=?,start_state='run_attached',updated_at=?
		WHERE source_id=? AND actor_id=? AND import_attempt=? AND start_state='start_unknown' AND run_id=''
		AND EXISTS(SELECT 1 FROM clipping_sources WHERE id=? AND kind='youtube_original_file' AND status='importing'
			AND rights_attested=1 AND import_attempts=? AND import_lease_until>?)`,
		runID, now, sourceID, actorID, expectedAttempts, sourceID, expectedAttempts, now)
	if err != nil {
		return clippingApifyRunState{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return clippingApifyRunState{}, err
	}
	if changed != 1 {
		return clippingApifyRunState{}, ErrClippingInvalidState
	}
	state.RunID = runID
	state.StartState = clippingApifyRunAttached
	if err := tx.Commit(); err != nil {
		return clippingApifyRunState{}, err
	}
	return state, nil
}

// ResolveClippingApifyStartRejected is valid only after a documented response
// proves that Apify did not accept the start request. Callers must never use it
// for transport errors, timeouts, 5xx responses, or malformed success bodies.
func (s *Store) ResolveClippingApifyStartRejected(sourceID string, expectedAttempts int, actorID string) (clippingApifyRunState, error) {
	if !safeClippingSourceID(sourceID) || expectedAttempts < 1 || expectedAttempts > MaxClippingImportAttempts || !validClippingApifyActorID(actorID) {
		return clippingApifyRunState{}, ErrClippingInvalidState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return clippingApifyRunState{}, err
	}
	defer tx.Rollback()
	state, err := clippingApifyAuthorizedState(tx, sourceID, expectedAttempts, actorID, time.Now().Unix())
	if err != nil {
		return clippingApifyRunState{}, err
	}
	if state.StartState != clippingApifyStartUnknown || state.RunID != "" {
		return clippingApifyRunState{}, ErrClippingInvalidState
	}
	now := time.Now().Unix()
	result, err := tx.Exec(`UPDATE clipping_apify_runs SET start_state='start_rejected',updated_at=?
		WHERE source_id=? AND actor_id=? AND import_attempt=? AND start_state='start_unknown' AND run_id=''
		AND EXISTS(SELECT 1 FROM clipping_sources WHERE id=? AND kind='youtube_original_file' AND status='importing'
			AND rights_attested=1 AND import_attempts=? AND import_lease_until>?)`,
		now, sourceID, actorID, expectedAttempts, sourceID, expectedAttempts, now)
	if err != nil {
		return clippingApifyRunState{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return clippingApifyRunState{}, err
	}
	if changed != 1 {
		return clippingApifyRunState{}, ErrClippingInvalidState
	}
	state.StartState = clippingApifyStartRejected
	if err := tx.Commit(); err != nil {
		return clippingApifyRunState{}, err
	}
	return state, nil
}

func (s *Store) GetClippingApifyRunState(sourceID string) (clippingApifyRunState, bool, error) {
	if !safeClippingSourceID(sourceID) {
		return clippingApifyRunState{}, false, ErrClippingSourceNotFound
	}
	state, found, err := scanClippingApifyRunState(s.db.QueryRow(`SELECT source_id,actor_id,import_attempt,start_intent_at,deadline_at,run_id,start_state
		FROM clipping_apify_runs WHERE source_id=?`, sourceID))
	return state, found, err
}

func validClippingApifyActorID(actorID string) bool {
	return strings.TrimSpace(actorID) == actorID && clippingApifyActorIDPattern.MatchString(actorID)
}

func validateClippingApifySourceLease(tx *sql.Tx, sourceID string, expectedAttempts int, now int64) error {
	var kind, status string
	var attempts, leaseUntil int64
	var rightsAttested int
	err := tx.QueryRow(`SELECT kind,status,rights_attested,import_attempts,import_lease_until FROM clipping_sources WHERE id=?`, sourceID).
		Scan(&kind, &status, &rightsAttested, &attempts, &leaseUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClippingSourceNotFound
	}
	if err != nil {
		return err
	}
	if kind != string(ClippingSourceYouTube) || status != string(ClippingSourceImporting) || rightsAttested != 1 ||
		attempts != int64(expectedAttempts) || leaseUntil <= now {
		return ErrClippingInvalidState
	}
	return nil
}

func clippingApifyAuthorizedState(tx *sql.Tx, sourceID string, expectedAttempts int, actorID string, now int64) (clippingApifyRunState, error) {
	if err := validateClippingApifySourceLease(tx, sourceID, expectedAttempts, now); err != nil {
		return clippingApifyRunState{}, err
	}
	state, found, err := scanClippingApifyRunState(tx.QueryRow(`SELECT source_id,actor_id,import_attempt,start_intent_at,deadline_at,run_id,start_state
		FROM clipping_apify_runs WHERE source_id=?`, sourceID))
	if err != nil {
		return clippingApifyRunState{}, err
	}
	if !found || state.ImportAttempt != expectedAttempts || state.ActorID != actorID {
		return clippingApifyRunState{}, ErrClippingInvalidState
	}
	return state, nil
}

func scanClippingApifyRunState(row interface{ Scan(...any) error }) (clippingApifyRunState, bool, error) {
	var state clippingApifyRunState
	var startState string
	err := row.Scan(&state.SourceID, &state.ActorID, &state.ImportAttempt, &state.StartIntentAt, &state.DeadlineAt, &state.RunID, &startState)
	if errors.Is(err, sql.ErrNoRows) {
		return clippingApifyRunState{}, false, nil
	}
	if err != nil {
		return clippingApifyRunState{}, false, err
	}
	state.StartState = clippingApifyStartState(startState)
	return state, true, nil
}
