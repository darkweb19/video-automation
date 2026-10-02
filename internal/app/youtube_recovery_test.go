package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func persistYouTubeRecoverySession(t *testing.T, fixture *youtubeBackendFixture, server *httptest.Server, status string, manualRestarts, consumedRestarts int, cancelRequested bool) YouTubeUpload {
	t.Helper()
	sessionURL := serverLocation(&http.Request{Host: strings.TrimPrefix(server.URL, "http://")}, "uploadType=resumable&upload_id=recovery-session")
	encryptedSession, err := fixture.security.EncryptSetting("youtube_upload_session_"+fixture.upload.ID, sessionURL)
	if err != nil {
		t.Fatal(err)
	}
	cancel := 0
	if cancelRequested {
		cancel = 1
	}
	_, err = fixture.store.db.Exec(`UPDATE youtube_uploads SET status=?,encrypted_session_url=?,manual_restart_count=?,manual_restart_consumed_count=?,cancel_requested=?,outcome_uncertain=?,next_attempt_at=0 WHERE id=?`, status, encryptedSession, manualRestarts, consumedRestarts, cancel, status == youtubeUploadAttentionRequired, fixture.upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	upload, err := fixture.store.YouTubeUpload(fixture.upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	return upload
}

func restartYouTubeRecoveryUpload(t *testing.T, fixture *youtubeBackendFixture) (YouTubeUpload, error) {
	t.Helper()
	fixture.store.youtubeMu.Lock()
	defer fixture.store.youtubeMu.Unlock()
	return fixture.store.restartYouTubeUploadLocked(fixture.upload.ID, fixture.source, time.Now().Unix())
}

func cancelYouTubeRecoveryUpload(t *testing.T, fixture *youtubeBackendFixture) (YouTubeUpload, error) {
	t.Helper()
	fixture.store.youtubeMu.Lock()
	defer fixture.store.youtubeMu.Unlock()
	return fixture.store.cancelYouTubeUploadLocked(fixture.upload.ID, time.Now().Unix())
}

func TestYouTubeRecoveryAdoptsVideoAfterLostFinalTransferResponse(t *testing.T) {
	video := []byte("framevault-test-video-content")
	var initiations, probes, chunks atomic.Int32
	serverHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			initiations.Add(1)
			t.Errorf("recovery started a second upload session: %s", r.URL.String())
			http.Error(w, "unexpected initiation", http.StatusBadRequest)
		case r.Method == http.MethodPut && strings.HasPrefix(r.Header.Get("Content-Range"), "bytes */"):
			if probes.Add(1) == 1 {
				w.Header().Set("Range", "bytes=0-0")
				w.WriteHeader(http.StatusPermanentRedirect)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"adopted-after-lost-response","status":{"privacyStatus":"private","uploadStatus":"uploaded"}}`)
		case r.Method == http.MethodPut:
			chunks.Add(1)
			if got, want := r.Header.Get("Content-Range"), fmt.Sprintf("bytes 1-%d/%d", len(video)-1, len(video)); got != want {
				t.Errorf("final chunk Content-Range=%q, want %q", got, want)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != string(video[1:]) {
				t.Errorf("final chunk body=%q err=%v", body, err)
			}
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("test server does not support dropping the final response")
				return
			}
			connection, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack final transfer response: %v", err)
				return
			}
			_ = connection.Close()
		default:
			t.Errorf("unexpected recovery request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	})
	fixture, server := newYouTubeBackendFixture(t, serverHandler)
	started := persistYouTubeRecoverySession(t, fixture, server, youtubeUploadSending, 0, 0, false)
	processor := fixture.processor()
	processor.processYouTubeUpload(context.Background(), started)
	interrupted, err := fixture.store.YouTubeUpload(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if interrupted.Status != youtubeUploadSending || interrupted.YouTubeVideoID != "" || interrupted.NextAttemptAt <= time.Now().Unix() {
		t.Fatalf("lost response did not preserve the resumable job for recovery: %#v", interrupted)
	}

	processor.processYouTubeUpload(context.Background(), interrupted)
	recovered, err := fixture.store.YouTubeUpload(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != youtubeUploadProcessing || recovered.YouTubeVideoID != "adopted-after-lost-response" || recovered.Progress != 100 {
		t.Fatalf("recovery did not adopt the completed remote video: %#v", recovered)
	}
	if initiations.Load() != 0 || probes.Load() != 2 || chunks.Load() != 1 {
		t.Fatalf("initiation/probe/chunk calls=%d/%d/%d", initiations.Load(), probes.Load(), chunks.Load())
	}
}

func TestYouTubeRecoveryRequiresOneShotApprovalAfterSessionExpiry(t *testing.T) {
	var initiations, probes atomic.Int32
	serverHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			initiations.Add(1)
			w.Header().Set("Location", serverLocation(r, "uploadType=resumable&upload_id=approved-replacement"))
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodPut || !strings.HasPrefix(r.Header.Get("Content-Range"), "bytes */") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
			return
		}
		probes.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	fixture, server := newYouTubeBackendFixture(t, serverHandler)
	started := persistYouTubeRecoverySession(t, fixture, server, youtubeUploadSending, 0, 0, false)
	processor := fixture.processor()
	processor.processYouTubeUpload(context.Background(), started)
	attention, err := fixture.store.YouTubeUpload(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if attention.Status != youtubeUploadAttentionRequired || !attention.OutcomeUncertain || initiations.Load() != 0 {
		t.Fatalf("expired session without approval was retried unsafely: upload=%#v initiations=%d", attention, initiations.Load())
	}
	processor.processYouTubeUpload(context.Background(), attention)
	if initiations.Load() != 0 || probes.Load() != 1 {
		t.Fatalf("attention-required job retried without approval: initiation/probes=%d/%d", initiations.Load(), probes.Load())
	}

	approved, err := restartYouTubeRecoveryUpload(t, fixture)
	if err != nil {
		t.Fatalf("explicit restart approval failed: %v", err)
	}
	if approved.Status != youtubeUploadSending || approved.ManualRestartCount != 1 || approved.EncryptedSessionURL == "" {
		t.Fatalf("approved job did not keep its original resumable session: %#v", approved)
	}
	processor.processYouTubeUpload(context.Background(), approved)
	queued, err := fixture.store.YouTubeUpload(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != youtubeUploadQueued || queued.ManualRestartCount != 1 || queued.ManualRestartConsumed != 1 || queued.EncryptedSessionURL != "" || initiations.Load() != 0 || probes.Load() != 2 {
		t.Fatalf("approved expired session was not consumed before retry: upload=%#v init/probes=%d/%d", queued, initiations.Load(), probes.Load())
	}
	processor.processYouTubeUpload(context.Background(), queued)
	startedAgain, err := fixture.store.YouTubeUpload(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if startedAgain.Status != youtubeUploadSending || startedAgain.EncryptedSessionURL == "" || initiations.Load() != 1 {
		t.Fatalf("explicit approval did not permit exactly one new session: upload=%#v initiations=%d", startedAgain, initiations.Load())
	}
	processor.processYouTubeUpload(context.Background(), startedAgain)
	attentionAgain, err := fixture.store.YouTubeUpload(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if attentionAgain.Status != youtubeUploadAttentionRequired || attentionAgain.ManualRestartCount != 1 || attentionAgain.ManualRestartConsumed != 1 || initiations.Load() != 1 || probes.Load() != 3 {
		t.Fatalf("expired retry reused its consumed approval: upload=%#v calls=%d/%d", attentionAgain, initiations.Load(), probes.Load())
	}
}

func TestYouTubeRecoveryConsumesApprovalBeforeASecondSessionExpiry(t *testing.T) {
	video := []byte("framevault-test-video-content")
	var initiations, probes, chunks atomic.Int32
	serverHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			initiations.Add(1)
			t.Errorf("consumed approval caused an unapproved new initiation")
			http.Error(w, "unexpected initiation", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPut {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Range"), "bytes */") {
			if probes.Add(1) == 2 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Range", "bytes=0-0")
			w.WriteHeader(http.StatusPermanentRedirect)
			return
		}
		chunks.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(video)-1))
		w.WriteHeader(http.StatusPermanentRedirect)
	})
	fixture, server := newYouTubeBackendFixture(t, serverHandler)
	attention := persistYouTubeRecoverySession(t, fixture, server, youtubeUploadAttentionRequired, 0, 0, false)
	if _, err := fixture.store.db.Exec(`UPDATE youtube_uploads SET error='Check YouTube Studio before restarting.' WHERE id=?`, attention.ID); err != nil {
		t.Fatal(err)
	}
	approved, err := restartYouTubeRecoveryUpload(t, fixture)
	if err != nil {
		t.Fatalf("explicit restart approval failed: %v", err)
	}
	processor := fixture.processor()
	processor.processYouTubeUpload(context.Background(), approved)
	partial, err := fixture.store.YouTubeUpload(attention.ID)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != youtubeUploadSending || partial.ManualRestartCount != 1 || partial.ManualRestartConsumed != 1 {
		t.Fatalf("server-confirmed resumable progress did not consume approval once: %#v", partial)
	}
	processor.processYouTubeUpload(context.Background(), partial)
	expiredAgain, err := fixture.store.YouTubeUpload(attention.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expiredAgain.Status != youtubeUploadAttentionRequired || !expiredAgain.OutcomeUncertain || expiredAgain.ManualRestartConsumed != 1 || initiations.Load() != 0 || probes.Load() != 2 || chunks.Load() != 1 {
		t.Fatalf("expired session reused a consumed approval: upload=%#v calls=%d/%d/%d", expiredAgain, initiations.Load(), probes.Load(), chunks.Load())
	}
	processor.processYouTubeUpload(context.Background(), expiredAgain)
	if initiations.Load() != 0 || probes.Load() != 2 {
		t.Fatalf("attention-required upload continued without another approval: initiation/probes=%d/%d", initiations.Load(), probes.Load())
	}
}

func TestYouTubeRecoveryCancellationRacesFinalProbe(t *testing.T) {
	t.Run("completion preserves the remote video", func(t *testing.T) {
		var initiations, probes, chunks atomic.Int32
		probeEntered := make(chan struct{})
		releaseProbe := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releaseProbe) }) }
		defer release()
		serverHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				initiations.Add(1)
				t.Errorf("cancelled transfer started another initiation")
				http.Error(w, "unexpected initiation", http.StatusBadRequest)
				return
			}
			if strings.HasPrefix(r.Header.Get("Content-Range"), "bytes */") {
				probes.Add(1)
				close(probeEntered)
				<-releaseProbe
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"id":"video-won-cancel-race","status":{"privacyStatus":"private","uploadStatus":"uploaded"}}`)
				return
			}
			chunks.Add(1)
			t.Errorf("sent chunk after server confirmed completion")
			http.Error(w, "unexpected chunk", http.StatusBadRequest)
		})
		fixture, server := newYouTubeBackendFixture(t, serverHandler)
		upload := persistYouTubeRecoverySession(t, fixture, server, youtubeUploadSending, 0, 0, false)
		done := make(chan struct{})
		go func() {
			fixture.processor().processYouTubeUpload(context.Background(), upload)
			close(done)
		}()
		select {
		case <-probeEntered:
		case <-time.After(3 * time.Second):
			t.Fatal("worker did not reach the final session probe")
		}
		cancelled, err := cancelYouTubeRecoveryUpload(t, fixture)
		if err != nil || !cancelled.CancelRequested {
			t.Fatalf("could not persist cancellation during completion probe: %#v err=%v", cancelled, err)
		}
		release()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("worker did not finish after final completion response")
		}
		recovered, err := fixture.store.YouTubeUpload(upload.ID)
		if err != nil {
			t.Fatal(err)
		}
		if recovered.Status != youtubeUploadProcessing || recovered.YouTubeVideoID != "video-won-cancel-race" || recovered.CancelRequested {
			t.Fatalf("cancellation overwrote confirmed completion: %#v", recovered)
		}
		if initiations.Load() != 0 || probes.Load() != 1 || chunks.Load() != 0 {
			t.Fatalf("initiation/probe/chunk calls=%d/%d/%d", initiations.Load(), probes.Load(), chunks.Load())
		}
	})

	t.Run("incomplete probe cancels before sending bytes", func(t *testing.T) {
		var initiations, probes, chunks atomic.Int32
		probeEntered := make(chan struct{})
		releaseProbe := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releaseProbe) }) }
		defer release()
		serverHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				initiations.Add(1)
				t.Errorf("cancelled transfer started another initiation")
				http.Error(w, "unexpected initiation", http.StatusBadRequest)
				return
			}
			if strings.HasPrefix(r.Header.Get("Content-Range"), "bytes */") {
				probes.Add(1)
				close(probeEntered)
				<-releaseProbe
				w.Header().Set("Range", "bytes=0-0")
				w.Header().Set("Range", "bytes=0-0")
				w.WriteHeader(http.StatusPermanentRedirect)
				return
			}
			chunks.Add(1)
			t.Errorf("sent bytes after cancellation was persisted")
			http.Error(w, "unexpected chunk", http.StatusBadRequest)
		})
		fixture, server := newYouTubeBackendFixture(t, serverHandler)
		upload := persistYouTubeRecoverySession(t, fixture, server, youtubeUploadSending, 0, 0, false)
		done := make(chan struct{})
		go func() {
			fixture.processor().processYouTubeUpload(context.Background(), upload)
			close(done)
		}()
		select {
		case <-probeEntered:
		case <-time.After(3 * time.Second):
			t.Fatal("worker did not reach the incomplete session probe")
		}
		requested, err := cancelYouTubeRecoveryUpload(t, fixture)
		if err != nil || !requested.CancelRequested {
			t.Fatalf("could not persist cancellation during incomplete probe: %#v err=%v", requested, err)
		}
		release()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("worker did not finish after incomplete probe response")
		}
		canceled, err := fixture.store.YouTubeUpload(upload.ID)
		if err != nil {
			t.Fatal(err)
		}
		if canceled.Status != youtubeUploadCanceled || canceled.EncryptedSessionURL != "" || canceled.CancelRequested {
			t.Fatalf("incomplete transfer did not stop cleanly: %#v", canceled)
		}
		if initiations.Load() != 0 || probes.Load() != 1 || chunks.Load() != 0 {
			t.Fatalf("initiation/probe/chunk calls=%d/%d/%d", initiations.Load(), probes.Load(), chunks.Load())
		}
	})
}
