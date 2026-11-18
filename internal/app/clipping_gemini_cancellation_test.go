package app

import (
	"context"
	"testing"
	"time"
)

func TestClippingGeminiCancellationRegistryIsSharedAcrossWrappers(t *testing.T) {
	registry := newClippingGeminiCancellationRegistry()
	dashboardStoreView := registry
	processorStoreView := registry

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, registered := processorStoreView.register("clipjob_one", "clipatt_one", cancel)
	if !registered {
		t.Fatal("processor did not register active attempt")
	}
	if !dashboardStoreView.cancelJob("clipjob_one") {
		t.Fatal("dashboard-side registry did not find processor attempt")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("registered inference context was not canceled")
	}
	release()
	if dashboardStoreView.cancelJob("clipjob_one") {
		t.Fatal("completed attempt remained registered")
	}
}

func TestStoreOwnsOneSharedGeminiCancellationRegistry(t *testing.T) {
	store := newTestStore(t)
	dashboardView := store.clippingGeminiCancellationRegistry()
	processorView := store.clippingGeminiCancellationRegistry()
	if dashboardView == nil || dashboardView != processorView {
		t.Fatal("Store wrappers did not share the same Gemini cancellation registry")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, ok := processorView.register("clipjob_store", "clipatt_store", cancel)
	if !ok {
		t.Fatal("processor could not register attempt")
	}
	defer release()
	if !dashboardView.cancelJob("clipjob_store") {
		t.Fatal("request-side registry did not find processor attempt")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Store-owned cancellation did not reach active inference")
	}
}

func TestClippingGeminiCancellationRegistryFencesDuplicateAndStaleRelease(t *testing.T) {
	registry := newClippingGeminiCancellationRegistry()
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	firstRelease, ok := registry.register("clipjob_one", "clipatt_one", firstCancel)
	if !ok {
		t.Fatal("first registration failed")
	}
	if _, ok := registry.register("clipjob_one", "clipatt_two", func() {}); ok {
		t.Fatal("registry accepted two active attempts for one job")
	}
	firstRelease()
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	secondRelease, ok := registry.register("clipjob_one", "clipatt_two", secondCancel)
	if !ok {
		t.Fatal("second registration failed after release")
	}
	firstRelease()
	if !registry.cancelJob("clipjob_one") {
		t.Fatal("stale release removed the newer registration")
	}
	select {
	case <-secondCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("newer attempt was not canceled")
	}
	select {
	case <-firstCtx.Done():
		t.Fatal("stale release unexpectedly canceled the old context")
	default:
	}
	secondRelease()
}

func TestClippingGeminiCancellationRegistryHandlesMultipleJobIDs(t *testing.T) {
	registry := newClippingGeminiCancellationRegistry()
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	firstRelease, firstOK := registry.register("clipjob_one", "clipatt_one", firstCancel)
	secondRelease, secondOK := registry.register("clipjob_two", "clipatt_two", secondCancel)
	if !firstOK || !secondOK {
		t.Fatal("registry rejected independent jobs")
	}
	if canceled := registry.cancelJobs([]string{"clipjob_one", "clipjob_missing", "clipjob_two"}); canceled != 2 {
		t.Fatalf("cancelJobs returned %d, want 2", canceled)
	}
	for name, ctx := range map[string]context.Context{"first": firstCtx, "second": secondCtx} {
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Errorf("%s attempt was not canceled", name)
		}
	}
	firstRelease()
	secondRelease()
}
