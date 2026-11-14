package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLegacyUnknownCallbackExpiryNeverReleasesClaimsOrCapabilities(t *testing.T) {
	for _, status := range []string{"running", "uncertain", "canceled", "failed"} {
		for _, expiry := range []string{"zero_lease", "negative_lease", "overflow_lease", "text_lease", "invalid_index", "text_index"} {
			t.Run(status+"/"+expiry, func(t *testing.T) {
				f := newLegacyInflightFixture(t)
				now := time.Now().Unix()
				jobStatus, attemptStatus := status, status
				if status == "uncertain" {
					jobStatus, attemptStatus = "running", "expired"
				}
				if _, err := f.store.db.Exec(`UPDATE clipping_jobs SET status=? WHERE id=?`, jobStatus, f.jobID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.db.Exec(`UPDATE clipping_stages SET status=?,lease_expires_at=1 WHERE job_id=?`, status, f.jobID); err != nil {
					t.Fatal(err)
				}
				lease := int64(0)
				switch expiry {
				case "negative_lease":
					lease = -1
				case "overflow_lease":
					lease = math.MaxInt64
				case "invalid_index", "text_index":
					lease = now - 100_000
				}
				if _, err := f.store.db.Exec(`UPDATE clipping_stage_attempts SET status=?,lease_expires_at=? WHERE attempt_id=?`, attemptStatus, lease, f.attemptID); err != nil {
					t.Fatal(err)
				}
				if expiry == "text_lease" {
					if _, err := f.store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at='invalid' WHERE attempt_id=?`, f.attemptID); err != nil {
						t.Fatal(err)
					}
				}
				if expiry == "invalid_index" || expiry == "text_index" {
					if _, err := f.store.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
						t.Fatal(err)
					}
					indexValue := any(int64(0))
					if expiry == "text_index" {
						indexValue = "invalid"
					}
					if _, err := f.store.db.Exec(`UPDATE clipping_capability_expiry_index SET expires_at=? WHERE attempt_id=?`, indexValue, f.attemptID); err != nil {
						t.Fatal(err)
					}
					if _, err := f.store.db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
						t.Fatal(err)
					}
				} else if _, err := f.store.db.Exec(`DELETE FROM clipping_capability_expiry_index WHERE attempt_id=?`, f.attemptID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.db.Exec(`UPDATE clipping_sources SET retain_until=1,media_lease_until=0 WHERE id=?`, f.sourceID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.CleanupExpiredClippingCapabilities(now, 100); err != nil {
					t.Fatal(err)
				}
				if err := f.store.RecoverExpiredClippingStages(now); err != nil {
					t.Fatal(err)
				}
				var claimCount, capabilityCount int
				if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM clipping_analysis_claims WHERE job_id=?`, f.jobID).Scan(&claimCount); err != nil {
					t.Fatal(err)
				}
				if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key=?`, clippingCapabilitySettingKey(f.jobID, f.attemptID, clippingCapabilityCallback)).Scan(&capabilityCount); err != nil {
					t.Fatal(err)
				}
				var reference string
				if err := f.store.db.QueryRow(`SELECT reconciliation_reference FROM clipping_stage_attempts WHERE attempt_id=?`, f.attemptID).Scan(&reference); err != nil {
					t.Fatal(err)
				}
				inventory, err := f.store.LegacyClippingDrainInventory(now)
				if err != nil || inventory.UnknownCallbackExpiry != 1 || claimCount != 1 || capabilityCount != 1 || reference != "" {
					t.Fatalf("unknown expiry resolved: inventory=%+v claims=%d caps=%d ref=%q error=%v", inventory, claimCount, capabilityCount, reference, err)
				}
				if expired, err := f.store.ExpiredClippingSources(now, 100); err != nil || len(expired) != 0 {
					t.Fatalf("unknown expiry did not protect media: sources=%+v error=%v", expired, err)
				}
			})
		}
	}
}

func TestExpiredIndexedTerminalAttemptsKeepProofThroughCleanupAndRecovery(t *testing.T) {
	for _, status := range []string{"running", "failed", "canceled"} {
		t.Run(status, func(t *testing.T) {
			f := newLegacyInflightFixture(t)
			now := time.Now()
			for _, update := range []struct {
				query string
				args  []any
			}{
				{`UPDATE clipping_jobs SET status=? WHERE id=?`, []any{status, f.jobID}},
				{`UPDATE clipping_stages SET status=?,lease_expires_at=1 WHERE job_id=?`, []any{status, f.jobID}},
				{`UPDATE clipping_stage_attempts SET status=?,lease_expires_at=0 WHERE attempt_id=?`, []any{status, f.attemptID}},
				{`UPDATE clipping_capability_expiry_index SET expires_at=? WHERE attempt_id=?`, []any{now.Unix() - 1, f.attemptID}},
				{`UPDATE clipping_sources SET retain_until=1,media_lease_until=0 WHERE id=?`, []any{f.sourceID}},
			} {
				if _, err := f.store.db.Exec(update.query, update.args...); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.store.CleanupExpiredClippingCapabilities(now.Unix(), 100); err != nil {
				t.Fatal(err)
			}
			if err := f.store.RecoverExpiredClippingStages(now.Unix()); err != nil {
				t.Fatal(err)
			}
			var claims, indexes int
			if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM clipping_analysis_claims WHERE job_id=?`, f.jobID).Scan(&claims); err != nil {
				t.Fatal(err)
			}
			if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM clipping_capability_expiry_index WHERE job_id=? AND attempt_id=?`, f.jobID, f.attemptID).Scan(&indexes); err != nil {
				t.Fatal(err)
			}
			inventory, err := f.store.LegacyClippingDrainInventory(now.Unix())
			if err != nil || claims != 0 || indexes != 1 || inventory.UnknownCallbackExpiry != 0 || inventory.UnexpiredCallbackWindows != 0 {
				t.Fatalf("cleanup lost expiry evidence: claims=%d indexes=%d inventory=%+v error=%v", claims, indexes, inventory, err)
			}
			cleaned, err := NewClippingAcquisition(f.store, nil).CleanupExpiredSources(context.Background(), now)
			if err != nil || cleaned != 1 {
				t.Fatalf("expired indexed source cleanup=%d error=%v", cleaned, err)
			}
		})
	}
}

func TestMalformedLegacyExpiryDoesNotBlockBackfillOrRetention(t *testing.T) {
	f := newLegacyInflightFixture(t)
	now := time.Now()
	security, err := NewSecurity(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`DELETE FROM clipping_capability_expiry_index WHERE attempt_id=?`, f.attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=0 WHERE attempt_id=?`, f.attemptID); err != nil {
		t.Fatal(err)
	}
	unknownKey := clippingCapabilitySettingKey(f.jobID, f.attemptID, clippingCapabilityCallback+"_expiry")
	if err := f.store.SetSetting(unknownKey, "corrupt-ciphertext"); err != nil {
		t.Fatal(err)
	}
	// Force the valid row onto a later one-item page than the unknown row.
	var name, jobID, attemptID string
	for i := 0; i < 1000; i++ {
		candidate := fmt.Sprintf("page-%d", i)
		job, attempt := legacyDrainFixtureIDs(candidate)
		if job > f.jobID {
			name, jobID, attemptID = candidate, job, attempt
			break
		}
	}
	if name == "" {
		t.Fatal("could not create ordered fixture")
	}
	legacyDrainAttemptFixture(t, f.store, name, "modal_legacy", "failed", "failed", now.Unix()-100_000, nil)
	key := clippingCapabilitySettingKey(jobID, attemptID, clippingCapabilityCallback+"_expiry")
	ciphertext, err := security.EncryptSetting(key, fmt.Sprint(now.Unix()-1))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetSetting(key, ciphertext); err != nil {
		t.Fatal(err)
	}
	app := &dashboardApp{store: f.store, security: security, clippingAcquisition: NewClippingAcquisition(f.store, security)}
	if cleaned, err := app.cleanupExpiredClippingCapabilities(now.Unix(), 1); err != nil || cleaned != 1 {
		t.Fatalf("backfill beyond unknown record cleaned=%d error=%v", cleaned, err)
	}
	if _, err := f.store.Setting(unknownKey); err != nil {
		t.Fatalf("unknown capability was removed: %v", err)
	}
	other := makeReadyClippingSource(t, f.store, 60_000)
	if _, err := f.store.db.Exec(`UPDATE clipping_sources SET retain_until=1 WHERE id=?`, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := (&Processor{app: app}).cleanupClippingSources(context.Background()); err != nil {
		t.Fatalf("unrelated retention was blocked by unknown expiry: %v", err)
	}
	if _, err := os.Stat(other.StoragePath); !os.IsNotExist(err) {
		t.Fatalf("unrelated source was not cleaned: %v", err)
	}
	inventory, err := f.store.LegacyClippingDrainInventory(now.Unix())
	if err != nil || inventory.UnknownCallbackExpiry != 1 {
		t.Fatalf("unknown expiry disappeared from inventory: %+v error=%v", inventory, err)
	}
}

func TestLegacyTerminalCallbackWindowsProtectAutomaticRetention(t *testing.T) {
	for _, status := range []string{"completed", "failed", "canceled"} {
		for _, invalid := range []bool{false, true} {
			t.Run(status+map[bool]string{true: "/unknown", false: "/unexpired"}[invalid], func(t *testing.T) {
				f := newLegacyInflightFixture(t)
				if _, err := f.store.db.Exec(`UPDATE clipping_jobs SET status=? WHERE id=?`, status, f.jobID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.db.Exec(`UPDATE clipping_stages SET status=? WHERE job_id=?`, status, f.jobID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.db.Exec(`UPDATE clipping_sources SET retain_until=1,media_lease_until=0 WHERE id=?`, f.sourceID); err != nil {
					t.Fatal(err)
				}
				if invalid {
					if _, err := f.store.db.Exec(`DELETE FROM clipping_capability_expiry_index WHERE attempt_id=?`, f.attemptID); err != nil {
						t.Fatal(err)
					}
					if _, err := f.store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=0 WHERE attempt_id=?`, f.attemptID); err != nil {
						t.Fatal(err)
					}
				}
				source, err := f.store.ClippingSource(f.sourceID)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.store.ExpireClippingSource(f.sourceID, time.Now().Unix()); err == nil {
					t.Fatal("retention deleted a source with an open or unknown callback window")
				}
				if _, err := os.Stat(source.StoragePath); err != nil {
					t.Fatalf("retention removed source media: %v", err)
				}
				var claims int
				if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM clipping_analysis_claims WHERE job_id=?`, f.jobID).Scan(&claims); err != nil || claims != 1 {
					t.Fatalf("retention removed claim=%d error=%v", claims, err)
				}
			})
		}
	}
}

func TestLegacyRecoveredJobDoesNotOfferRetiredRetry(t *testing.T) {
	store, _, handler, cookie := clippingDashboardFixture(t)
	now := time.Now().Unix()
	elapsed := now - 1
	legacyDrainAttemptFixture(t, store, "expired-guidance", "modal_legacy", "running", "expired", now-100_000, &elapsed)
	jobID, _ := legacyDrainFixtureIDs("expired-guidance")
	if _, err := store.db.Exec(`UPDATE clipping_stages SET status='uncertain' WHERE job_id=?`, jobID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverExpiredClippingStages(now); err != nil {
		t.Fatal(err)
	}
	response := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/jobs/"+jobID, "", cookie, "")
	if response.Code != http.StatusOK || !json.Valid(response.Body.Bytes()) || strings.Contains(strings.ToLower(response.Body.String()), "retry is available") || !strings.Contains(strings.ToLower(response.Body.String()), "retired") {
		t.Fatalf("recovered job guidance=%d %s", response.Code, response.Body.String())
	}
}

func TestUnknownLegacyExpiryDoesNotBlockUnrelatedRetention(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().Unix()
	// More unknown records than a one-result page verifies that the scan
	// continues past a full page of protected sources.
	for i := 0; i < 6; i++ {
		name := string(rune('a' + i))
		legacyDrainAttemptFixture(t, store, name, "modal_legacy", "canceled", "canceled", 0, nil)
		jobID, _ := legacyDrainFixtureIDs(name)
		if _, err := store.db.Exec(`UPDATE clipping_stages SET status='canceled' WHERE job_id=?`, jobID); err != nil {
			t.Fatal(err)
		}
	}
	clean := makeReadyClippingSource(t, store, 60_000)
	if _, err := store.db.Exec(`UPDATE clipping_sources SET retain_until=1 WHERE id=?`, clean.ID); err != nil {
		t.Fatal(err)
	}
	expired, err := store.ExpiredClippingSources(now, 1)
	if err != nil || len(expired) != 1 || expired[0].ID != clean.ID {
		t.Fatalf("unknown expiry blocked unrelated retention: sources=%+v error=%v", expired, err)
	}
	if err := store.ExpireClippingSource(clean.ID, now); err != nil {
		t.Fatalf("could not expire unrelated source: %v", err)
	}
	if _, err := store.db.Exec(`DROP TABLE clipping_capability_expiry_index`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExpiredClippingSources(now, 100); err == nil {
		t.Fatal("real database failure was treated as an unknown expiry")
	}
}

func TestRetentionRechecksCallbackExpiryWithoutAbortingBatch(t *testing.T) {
	for _, change := range []string{"unknown", "open", "lease", "retention"} {
		t.Run(change, func(t *testing.T) {
			f := newLegacyInflightFixture(t)
			now := time.Now()
			if _, err := f.store.db.Exec(`UPDATE clipping_jobs SET status='failed' WHERE id=?`, f.jobID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.db.Exec(`UPDATE clipping_stages SET status='failed' WHERE job_id=?`, f.jobID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.db.Exec(`UPDATE clipping_sources SET retain_until=1,media_lease_until=0 WHERE id=?`, f.sourceID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.db.Exec(`UPDATE clipping_capability_expiry_index SET expires_at=? WHERE attempt_id=?`, now.Unix()-1, f.attemptID); err != nil {
				t.Fatal(err)
			}
			protected, err := f.store.ClippingSource(f.sourceID)
			if err != nil {
				t.Fatal(err)
			}
			other := makeReadyClippingSource(t, f.store, 60_000)
			if _, err := f.store.db.Exec(`UPDATE clipping_sources SET retain_until=1 WHERE id=?`, other.ID); err != nil {
				t.Fatal(err)
			}
			// Simulate a change after the retention candidate snapshot was read.
			switch change {
			case "unknown":
				if _, err := f.store.db.Exec(`DELETE FROM clipping_capability_expiry_index WHERE attempt_id=?`, f.attemptID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.db.Exec(`UPDATE clipping_stage_attempts SET lease_expires_at=0 WHERE attempt_id=?`, f.attemptID); err != nil {
					t.Fatal(err)
				}
			case "open":
				if _, err := f.store.db.Exec(`UPDATE clipping_capability_expiry_index SET expires_at=? WHERE attempt_id=?`, now.Unix()+300, f.attemptID); err != nil {
					t.Fatal(err)
				}
			case "lease":
				if _, err := f.store.db.Exec(`UPDATE clipping_sources SET import_lease_until=? WHERE id=?`, now.Unix()+300, f.sourceID); err != nil {
					t.Fatal(err)
				}
			case "retention":
				if _, err := f.store.db.Exec(`UPDATE clipping_sources SET retain_until=? WHERE id=?`, now.Unix()+300, f.sourceID); err != nil {
					t.Fatal(err)
				}
			}
			cleaned, err := NewClippingAcquisition(f.store, nil).cleanupExpiredSources(context.Background(), now, []ClippingSource{protected, other})
			if err != nil || cleaned != 1 {
				t.Fatalf("cleanup count=%d error=%v", cleaned, err)
			}
			if _, err := os.Stat(protected.StoragePath); err != nil {
				t.Fatalf("protected source media was removed: %v", err)
			}
			if _, err := os.Stat(other.StoragePath); !os.IsNotExist(err) {
				t.Fatalf("unrelated expired media remains: %v", err)
			}
			var claims int
			if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM clipping_analysis_claims WHERE job_id=?`, f.jobID).Scan(&claims); err != nil || claims != 1 {
				t.Fatalf("protected claims=%d error=%v", claims, err)
			}
		})
	}
}
