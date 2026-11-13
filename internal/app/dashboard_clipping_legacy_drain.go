package app

import (
	"net/http"
	"time"
)

// clippingLegacyDrain only reports persisted state. It neither confirms that
// external workers have stopped nor decides whether retirement is safe.
func (a *dashboardApp) clippingLegacyDrain(w http.ResponseWriter, _ *http.Request) {
	inventory, err := a.store.LegacyClippingDrainInventory(time.Now().Unix())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not inspect legacy clipping drain")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, inventory)
}
