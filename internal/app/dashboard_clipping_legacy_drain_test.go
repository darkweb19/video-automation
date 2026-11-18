package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLegacyClippingDrainEndpointIsAuthenticatedAndReadOnly(t *testing.T) {
	store, _, handler, cookie := clippingDashboardFixture(t)
	issued := time.Now().Unix() + 300
	legacyDrainAttemptFixture(t, store, "operator", "modal_legacy", "canceled", "canceled", issued, &issued)
	path := "/api/clipping/legacy-drain"
	if response := clippingDashboardRequest(handler, http.MethodGet, path, "", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated inventory status=%d", response.Code)
	}
	var before, after int64
	if err := store.db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	response := clippingDashboardRequest(handler, http.MethodGet, path, "", cookie, "")
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("inventory status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var inventory ClippingLegacyDrainInventory
	if err := json.Unmarshal(response.Body.Bytes(), &inventory); err != nil || inventory.ObservedAt == 0 || inventory.UnexpiredCallbackWindows != 1 {
		t.Fatalf("inventory=%+v err=%v", inventory, err)
	}
	if strings.Contains(response.Body.String(), "operator") || strings.Contains(response.Body.String(), "fixture") {
		t.Fatalf("inventory exposed identifiers or paths: %s", response.Body.String())
	}
	if err := store.db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil || after != before {
		t.Fatalf("inventory changed database: before=%d after=%d error=%v", before, after, err)
	}
	if _, err := store.db.Exec(`DROP TABLE clipping_capability_expiry_index`); err != nil {
		t.Fatal(err)
	}
	response = clippingDashboardRequest(handler, http.MethodGet, path, "", cookie, "")
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "unexpired_callback_windows") {
		t.Fatalf("storage failure returned usable inventory: %d %s", response.Code, response.Body.String())
	}
}
