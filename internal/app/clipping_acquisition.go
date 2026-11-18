package app

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ClippingAcquisition now owns only durable source deletion and retention.
// It cannot create or execute legacy imports.
type ClippingAcquisition struct {
	store               *Store
	deleteOperationWait time.Duration
}

type clippingActiveOperation struct {
	cancel context.CancelFunc
	closer io.Closer
	done   chan struct{}
}

type clippingOperationRegistry struct {
	mu         sync.Mutex
	operations map[string]clippingActiveOperation
}

func NewClippingAcquisition(store *Store, _ *Security) *ClippingAcquisition {
	return &ClippingAcquisition{store: store}
}

func (a *ClippingAcquisition) CreatePublicImport(context.Context, bool, string, int64) (ClippingSource, error) {
	return ClippingSource{}, ErrClippingLegacyRetired
}

func (a *ClippingAcquisition) ProcessNextImport(context.Context) (bool, error) {
	return false, ErrClippingLegacyRetired
}

// DeleteSource stops any active import before installing a durable tombstone,
// then removes its local media. Job audit rows and callback capabilities stay
// available long enough for late worker callbacks to settle safely.
func (a *ClippingAcquisition) DeleteSource(ctx context.Context, sourceID string) ([]string, bool, error) {
	if a == nil || a.store == nil {
		return nil, false, errors.New("clipping source storage is unavailable")
	}
	done := a.cancelOperation(sourceID)
	cleanupPending := false
	if done != nil {
		waitFor := a.deleteOperationWait
		if waitFor <= 0 {
			waitFor = 5 * time.Second
		}
		select {
		case <-done:
		case <-time.After(waitFor):
			cleanupPending = true
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	jobIDs, err := a.store.DeleteClippingSource(sourceID)
	if err != nil {
		return nil, false, err
	}
	a.store.PublishClippingSource(sourceID)
	if done != nil {
		select {
		case <-done:
			cleanupPending = false
		default:
			cleanupPending = true
		}
	}
	if cleanupPending {
		// Leave cleanup_complete=false. The retention cleanup loop skips live
		// operations and removes the directory after this canceled importer exits.
		go a.cleanupDeletedSourceAfterOperation(sourceID, done)
		return jobIDs, true, nil
	}
	if err := a.removeSourceFiles(sourceID); err != nil {
		return jobIDs, false, err
	}
	if err := a.store.MarkClippingSourceCleaned(sourceID); err != nil {
		return jobIDs, false, err
	}
	a.store.PublishClippingSource(sourceID)
	return jobIDs, false, nil
}

func (a *ClippingAcquisition) cleanupDeletedSourceAfterOperation(sourceID string, done <-chan struct{}) {
	if done == nil {
		return
	}
	<-done
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	source, err := a.store.ClippingSource(sourceID)
	if err != nil || source.Status != ClippingSourceDeleted || source.CleanupComplete {
		return
	}
	if err := a.removeSourceFiles(sourceID); err != nil {
		return
	}
	if err := a.store.MarkClippingSourceCleaned(sourceID); err != nil {
		return
	}
	a.store.PublishClippingSource(sourceID)
}

// CancelSource is retained for import lifecycle callers. User initiated
// removal is a full source deletion, including its visible analysis results.
func (a *ClippingAcquisition) CancelSource(ctx context.Context, sourceID string) error {
	jobIDs, _, err := a.DeleteSource(ctx, sourceID)
	if a == nil || a.store == nil {
		return err
	}
	for _, jobID := range jobIDs {
		a.store.PublishClippingJob(jobID)
	}
	return err
}

// CleanupExpiredSources tombstones eligible rows before removing files, so a
// restart resumes cleanup from durable source state.
func (a *ClippingAcquisition) CleanupExpiredSources(ctx context.Context, now time.Time) (int, error) {
	if a == nil || a.store == nil {
		return 0, errors.New("clipping source storage is unavailable")
	}
	if now.IsZero() {
		now = time.Now()
	}
	sources, err := a.store.ExpiredClippingSources(now.Unix(), 100)
	if err != nil {
		return 0, err
	}
	return a.cleanupExpiredSources(ctx, now, sources)
}

// Recheck every source before deleting files. A callback expiry, lease, or job
// may change after the candidate snapshot; defer that source without blocking
// the remainder of the batch. Database and filesystem errors still surface.
func (a *ClippingAcquisition) cleanupExpiredSources(ctx context.Context, now time.Time, sources []ClippingSource) (int, error) {
	cleaned := 0
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return cleaned, err
		}
		if a.hasActiveOperation(source.ID) {
			continue
		}
		a.store.clippingMu.Lock()
		if source.Status != ClippingSourceDeleted {
			if err := a.store.ExpireClippingSource(source.ID, now.Unix()); err != nil {
				a.store.clippingMu.Unlock()
				if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrClippingSourceNotFound) ||
					errors.Is(err, ErrClippingInvalidState) || errors.Is(err, errClippingRetentionDeferred) {
					continue
				}
				return cleaned, err
			}
			a.store.PublishClippingSource(source.ID)
		}
		if err := a.removeSourceFiles(source.ID); err != nil {
			a.store.clippingMu.Unlock()
			return cleaned, err
		}
		if err := a.store.MarkClippingSourceCleaned(source.ID); err != nil {
			a.store.clippingMu.Unlock()
			return cleaned, err
		}
		a.store.PublishClippingSource(source.ID)
		a.store.clippingMu.Unlock()
		cleaned++
	}
	return cleaned, nil
}

func (a *ClippingAcquisition) removeSourceFiles(sourceID string) error {
	source, err := a.store.ClippingSource(sourceID)
	if err != nil {
		return err
	}
	if source.Kind == ClippingSourceYouTubeURL {
		if source.Status != ClippingSourceDeleted {
			return ErrClippingInvalidState
		}
		// URL references never own a media directory. Do not synthesize a path
		// or remove files that happen to have the same source ID.
		return nil
	}
	path, err := a.store.ClippingSourcePath(sourceID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		return err
	}
	if err := syncClippingDirectory(filepath.Dir(filepath.Dir(path))); err != nil {
		return err
	}
	return nil
}

func (a *ClippingAcquisition) beginOperation(parent context.Context, sourceID string, closer io.Closer) (context.Context, func(), error) {
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	registry := a.operationRegistry()
	registry.mu.Lock()
	if _, exists := registry.operations[sourceID]; exists {
		registry.mu.Unlock()
		cancel()
		return nil, nil, errors.New("source already has an active acquisition operation")
	}
	registry.operations[sourceID] = clippingActiveOperation{cancel: cancel, closer: closer, done: done}
	registry.mu.Unlock()
	finish := func() {
		registry.mu.Lock()
		if active, ok := registry.operations[sourceID]; ok && active.done == done {
			delete(registry.operations, sourceID)
		}
		close(done)
		registry.mu.Unlock()
		cancel()
	}
	return ctx, finish, nil
}

func (a *ClippingAcquisition) cancelOperation(sourceID string) <-chan struct{} {
	registry := a.operationRegistry()
	registry.mu.Lock()
	active, ok := registry.operations[sourceID]
	if ok {
		active.cancel()
	}
	registry.mu.Unlock()
	if ok && active.closer != nil {
		_ = active.closer.Close()
	}
	if !ok {
		return nil
	}
	return active.done
}

func (a *ClippingAcquisition) hasActiveOperation(sourceID string) bool {
	registry := a.operationRegistry()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	_, ok := registry.operations[sourceID]
	return ok
}

func (a *ClippingAcquisition) operationRegistry() *clippingOperationRegistry {
	a.store.clippingMu.Lock()
	defer a.store.clippingMu.Unlock()
	if a.store.clippingOperations == nil {
		a.store.clippingOperations = &clippingOperationRegistry{operations: make(map[string]clippingActiveOperation)}
	}
	if a.store.clippingOperations.operations == nil {
		a.store.clippingOperations.operations = make(map[string]clippingActiveOperation)
	}
	return a.store.clippingOperations
}
