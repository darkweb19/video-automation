//go:build unix

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestYouTubeDownloaderCancellationKillsProcessGroupAndCleansWorkspace(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	media := makeLocalYouTubeMP4(t, ffmpeg, false, false)
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	configureFakeYouTubeDownloader(t, acquisition, ffmpeg, media, "block", "")
	source := createYouTubeImport(t, acquisition)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		worked bool
		err    error
	}
	done := make(chan result, 1)
	go func() {
		worked, err := acquisition.ProcessNextImport(ctx)
		done <- result{worked: worked, err: err}
	}()
	started := media + ".started"
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake yt-dlp process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case outcome := <-done:
		if !outcome.worked || outcome.err != nil {
			t.Fatalf("canceled import result worked=%v error=%v", outcome.worked, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled yt-dlp process group did not stop")
	}
	paths, err := acquisition.sourcePaths(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(youtubeWorkspacePath(paths)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled download workspace was not cleaned: %v", err)
	}
	time.Sleep(2100 * time.Millisecond)
	if _, err := os.Stat(media + ".survived"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant process survived cancellation: %v", err)
	}
	loaded, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != ClippingSourceImporting {
		t.Fatalf("cancellation unexpectedly made source terminal: %s", loaded.Status)
	}
	if _, err := os.Stat(filepath.Dir(paths.final)); err != nil {
		t.Fatalf("source reservation directory disappeared after retry: %v", err)
	}
}
