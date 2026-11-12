package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestPrepareClippingGeminiSubmissionUsesProtectedURLOnly(t *testing.T) {
	for _, sourceKind := range []ClippingSourceKind{ClippingSourceYouTubeURL, ClippingSourceYouTube} {
		t.Run(string(sourceKind), func(t *testing.T) {
			store := newTestStore(t)
			security, err := NewSecurity(store)
			if err != nil {
				t.Fatal(err)
			}
			input := geminiJobInput()
			input.RetainUntil = time.Now().Add(time.Hour).Unix()
			if sourceKind == ClippingSourceYouTube {
				ready := makeReadyClippingSource(t, store, 120_000)
				ciphertext, err := security.EncryptSetting(clippingSourceURLSetting, input.YouTubeURL)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.Exec(`UPDATE clipping_sources SET kind='youtube_original_file',source_url=? WHERE id=?`, ciphertext, ready.ID); err != nil {
					t.Fatal(err)
				}
				input.SourceID, input.YouTubeURL, input.RetainUntil = ready.ID, "", 0
			}
			job, duplicate, err := store.CreateClippingGeminiJob(security, input, "submission-request-"+string(sourceKind))
			if err != nil || duplicate {
				t.Fatalf("create Gemini job: duplicate=%v error=%v", duplicate, err)
			}
			snapshot, err := store.ClippingGeminiRequestSnapshot(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			source, err := store.ClippingSource(job.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			// These file-backed values are intentionally unusable. Preparing a
			// Gemini request must not inspect or depend on them.
			source.StoragePath = "/missing/local/media/should-not-be-read"
			source.SHA256 = "not-a-media-hash"
			source.DurationMS = -99
			source.SizeBytes = -99
			prepared, err := prepareClippingGeminiSubmissionForJob(job, snapshot, source, security, time.Now())
			if err != nil {
				t.Fatalf("prepare remote request: %v", err)
			}
			if prepared.CanonicalYouTubeURL != "https://www.youtube.com/watch?v=abcdefghijk" || prepared.Fingerprint != snapshot.Fingerprint {
				t.Fatalf("prepared URL/fingerprint mismatch: %+v snapshot=%+v", prepared, snapshot)
			}
			if prepared.Content == nil || len(prepared.Content.Parts) != 2 || prepared.Content.Parts[0].FileData == nil ||
				prepared.Content.Parts[0].FileData.FileURI != prepared.CanonicalYouTubeURL || prepared.Content.Parts[0].InlineData != nil {
				t.Fatalf("Gemini request did not contain the direct remote YouTube URL: %#v", prepared.Content)
			}
			if strings.Contains(prepared.CanonicalYouTubeURL, "missing/local/media") || strings.Contains(prepared.CanonicalYouTubeURL, "not-a-media-hash") {
				t.Fatal("prepared request used local file identity")
			}
			if prepared.Content.Parts[0].MediaProcessing != genai.MediaProcessingStatic {
				t.Fatalf("media processing mode=%q", prepared.Content.Parts[0].MediaProcessing)
			}
		})
	}
}

func TestPrepareClippingGeminiSubmissionFailsClosedOnJobSourceOrSnapshotMismatch(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	input := geminiJobInput()
	job, _, err := store.CreateClippingGeminiJob(security, input, "submission-request-mismatch")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ClippingGeminiRequestSnapshot(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.ClippingSource(job.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	wrongJob := job
	wrongJob.ID = "clipjob_other"
	if _, err := prepareClippingGeminiSubmissionForJob(wrongJob, snapshot, source, security, now); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("job/snapshot mismatch: %v", err)
	}
	wrongSource := source
	wrongSource.ID = "clipsrc_other"
	if _, err := prepareClippingGeminiSubmissionForJob(job, snapshot, wrongSource, security, now); !errors.Is(err, ErrClippingInvalidState) {
		t.Fatalf("job/source mismatch: %v", err)
	}
	drifted := snapshot
	drifted.Config.ProjectID = "framevault-other-123"
	if _, err := prepareClippingGeminiSubmissionForJob(job, drifted, source, security, now); !errors.Is(err, errClippingGeminiConfigDrift) {
		t.Fatalf("config drift: %v", err)
	}
	changedURL, err := security.EncryptSetting(clippingSourceURLSetting, "https://www.youtube.com/watch?v=zyxwvutsrqp")
	if err != nil {
		t.Fatal(err)
	}
	changedSource := source
	changedSource.SourceURL = changedURL
	if _, err := prepareClippingGeminiSubmissionForJob(job, snapshot, changedSource, security, now); !errors.Is(err, errClippingGeminiConfigDrift) {
		t.Fatalf("URL/fingerprint drift: %v", err)
	}
}

func TestPrepareClippingGeminiSubmissionRejectsInaccessibleSourceBeforeSend(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	input := geminiJobInput()
	job, _, err := store.CreateClippingGeminiJob(security, input, "submission-request-inaccessible")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ClippingGeminiRequestSnapshot(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.ClippingSource(job.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ClippingSource)
		now    time.Time
	}{
		{name: "deleted", mutate: func(s *ClippingSource) { s.Status, s.DeletedAt = ClippingSourceDeleted, time.Now().Unix() }},
		{name: "rights missing", mutate: func(s *ClippingSource) { s.RightsAttested = false }},
		{name: "wrong kind", mutate: func(s *ClippingSource) { s.Kind = ClippingSourceUpload }},
		{name: "missing encrypted URL", mutate: func(s *ClippingSource) { s.SourceURL = "" }},
		{name: "bad ciphertext", mutate: func(s *ClippingSource) { s.SourceURL = "broken-ciphertext" }},
		{name: "retention expired", mutate: func(s *ClippingSource) { s.RetainUntil = 100 }, now: time.Unix(101, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := source
			tc.mutate(&mutated)
			now := tc.now
			if now.IsZero() {
				now = time.Now()
			}
			if _, err := prepareClippingGeminiSubmissionForJob(job, snapshot, mutated, security, now); !errors.Is(err, errClippingGeminiSourceInaccessible) {
				t.Fatalf("inaccessible source error=%v", err)
			}
		})
	}
}
