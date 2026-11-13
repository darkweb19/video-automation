package app

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLegacyUnknownCallbackExpiryNeverReleasesClaimsOrCapabilities(t *testing.T) {
	for _, status := range []string{"running", "uncertain", "canceled", "failed"} {
		for _, expiry := range []string{"zero_lease", "negative_lease", "overflow_lease", "invalid_index"} {
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
				case "invalid_index":
					lease = now - 100_000
				}
				if _, err := f.store.db.Exec(`UPDATE clipping_stage_attempts SET status=?,lease_expires_at=? WHERE attempt_id=?`, attemptStatus, lease, f.attemptID); err != nil {
					t.Fatal(err)
				}
				if expiry == "invalid_index" {
					if _, err := f.store.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
						t.Fatal(err)
					}
					if _, err := f.store.db.Exec(`UPDATE clipping_capability_expiry_index SET expires_at=0 WHERE attempt_id=?`, f.attemptID); err != nil {
						t.Fatal(err)
					}
					if _, err := f.store.db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
						t.Fatal(err)
					}
				} else if _, err := f.store.db.Exec(`DELETE FROM clipping_capability_expiry_index WHERE attempt_id=?`, f.attemptID); err != nil {
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
			})
		}
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
