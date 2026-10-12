package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClippingSourceDiagnosticsStayBoundedAndMonotonicAfterRestart(t *testing.T) {
	dataDir, store, _, acquisition := newClippingAcquisitionFixture(t)
	rawURL := "https://drive.google.com/file/d/id/view?resourcekey=diagnostic-secret-marker"
	source, err := acquisition.CreatePublicImport(context.Background(), true, rawURL, 0)
	if err != nil {
		t.Fatal(err)
	}
	initialUpdatedAt := source.UpdatedAt
	for i := 0; i < MaxClippingSourceDiagnostics+2; i++ {
		if err := store.AppendClippingSourceDiagnostic(source.ID, ClippingDiagnosticStageDownload, ClippingDiagnosticCompleted, ""); err != nil {
			t.Fatalf("append trace %d: %v", i, err)
		}
	}
	loaded, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Diagnostics) != MaxClippingSourceDiagnostics || loaded.Diagnostics[0].Sequence != 6 || loaded.Diagnostics[len(loaded.Diagnostics)-1].Sequence != 133 {
		t.Fatalf("bounded source history=%d first=%d last=%d", len(loaded.Diagnostics), loaded.Diagnostics[0].Sequence, loaded.Diagnostics[len(loaded.Diagnostics)-1].Sequence)
	}
	if loaded.UpdatedAt != initialUpdatedAt {
		t.Fatalf("diagnostic writes changed source lifecycle timestamp from %d to %d", initialUpdatedAt, loaded.UpdatedAt)
	}
	encoded, err := json.Marshal(clippingSourceSummary(loaded))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "diagnostic-secret-marker") || strings.Contains(string(encoded), loaded.SourceURL) || strings.Contains(string(encoded), loaded.StoragePath) {
		t.Fatalf("safe source snapshot contained sensitive data: %s", encoded)
	}

	stream, unsubscribe := store.events.subscribe()
	defer unsubscribe()
	store.PublishClippingSource(source.ID)
	select {
	case event := <-stream:
		var payload struct {
			Source clippingSourceView `json:"source"`
		}
		if err := json.Unmarshal(event.data, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Source.Diagnostics) != MaxClippingSourceDiagnostics || payload.Source.Diagnostics[len(payload.Source.Diagnostics)-1].Sequence != 133 {
			t.Fatalf("source event did not include recent trace: %+v", payload.Source.Diagnostics)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for source diagnostic EventSource snapshot")
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.AppendClippingSourceDiagnostic(source.ID, ClippingDiagnosticStageDownload, ClippingDiagnosticRetrying, "retry_scheduled"); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Diagnostics) != MaxClippingSourceDiagnostics || reopened.Diagnostics[0].Sequence != 7 || reopened.Diagnostics[len(reopened.Diagnostics)-1].Sequence != 134 {
		t.Fatalf("restart did not preserve monotonic bounded history: count=%d first=%d last=%d", len(reopened.Diagnostics), reopened.Diagnostics[0].Sequence, reopened.Diagnostics[len(reopened.Diagnostics)-1].Sequence)
	}
}

func TestClippingSourceListResponseIncludesSafeDiagnostics(t *testing.T) {
	store, _, handler, cookie := clippingDashboardFixture(t, true)
	source, err := store.CreateClippingSource(ClippingSourceCreate{
		Kind: ClippingSourceUpload, OriginalName: "meeting.mp4", MediaType: "video/mp4",
		DeclaredSizeBytes: 64, RightsAttested: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendClippingSourceDiagnostic(source.ID, ClippingDiagnosticStageReservation, ClippingDiagnosticCompleted, ""); err != nil {
		t.Fatal(err)
	}
	response := clippingDashboardRequest(handler, http.MethodGet, "/api/clipping/sources", "", cookie, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"diagnostics":[{"sequence":1,"stage":"source_reservation","status":"completed","at":`) {
		t.Fatalf("authenticated source list omitted diagnostics: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestClippingImportDiagnosticsClassifyRuntimeAndExtractorFailuresSafely(t *testing.T) {
	tests := []struct {
		name          string
		extractorBody string
		runtimePath   func(string) string
		stage         string
		status        ClippingSourceDiagnosticStatus
		code          string
	}{
		{
			name:        "runtime unavailable",
			runtimePath: func(directory string) string { return filepath.Join(directory, "missing-deno") },
			stage:       ClippingDiagnosticStageMetadata, status: ClippingDiagnosticFailed, code: "runtime_unavailable",
		},
		{
			name:          "extractor exit failure",
			extractorBody: "printf 'https://signed.example/media?secret=extractor-secret-marker\\n'\nprintf 'extractor-secret-marker\\n' >&2\nexit 17\n",
			runtimePath:   func(directory string) string { return writeFakeYouTubeExecutable(t, directory, "deno", "exit 0\n") },
			stage:         ClippingDiagnosticStageMetadata, status: ClippingDiagnosticRetrying, code: "extractor_exit_failure",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, store, _, acquisition := newClippingAcquisitionFixture(t)
			directory := t.TempDir()
			acquisition.youtubeExecutable = filepath.Join(directory, "fake-yt-dlp")
			acquisition.youtubeRuntime = "deno:" + test.runtimePath(directory)
			if test.extractorBody != "" {
				acquisition.youtubeExecutable = writeFakeYouTubeExecutable(t, directory, "yt-dlp", test.extractorBody)
			}
			acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
			source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/abcdefghijk?si=private-link-token", 0)
			if err != nil {
				t.Fatal(err)
			}
			if worked, err := acquisition.ProcessNextImport(context.Background()); err != nil || !worked {
				t.Fatalf("process import worked=%v error=%v", worked, err)
			}
			loaded, err := store.ClippingSource(source.ID)
			if err != nil {
				t.Fatal(err)
			}
			foundFailure := false
			for _, item := range loaded.Diagnostics {
				if item.Stage == test.stage && item.Status == test.status && item.Code == test.code {
					foundFailure = true
				}
			}
			if !foundFailure {
				t.Fatalf("missing safe diagnostic %s/%s/%s in %+v", test.stage, test.status, test.code, loaded.Diagnostics)
			}
			encoded, err := json.Marshal(clippingSourceSummary(loaded))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "private-link-token") || strings.Contains(string(encoded), "extractor-secret-marker") || strings.Contains(string(encoded), "signed.example") || strings.Contains(string(encoded), "stderr") {
				t.Fatalf("source diagnostic leaked private extractor data: %s", encoded)
			}
			if test.name == "runtime unavailable" && strings.Contains(string(encoded), "format_unavailable") {
				t.Fatalf("missing runtime was mislabeled as a format limitation: %s", encoded)
			}
		})
	}
}

func TestClippingDiagnosticInputRejectsUnknownStagesAndCodes(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://drive.google.com/file/d/id/view", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendClippingSourceDiagnostic(source.ID, "extractor_stderr", ClippingDiagnosticFailed, "download_failed"); err == nil {
		t.Fatal("unknown diagnostic stage was accepted")
	}
	if err := store.AppendClippingSourceDiagnostic(source.ID, ClippingDiagnosticStageDownload, ClippingDiagnosticFailed, "raw error with secret"); err == nil {
		t.Fatal("non-enumerated diagnostic code was accepted")
	}
}
