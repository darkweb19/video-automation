package app

import "net/http"

func (a *dashboardApp) retiredClippingRead(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusGone, "Legacy clipping analysis has been retired")
}

func (a *dashboardApp) retiredClippingMutation(w http.ResponseWriter, r *http.Request) {
	if !mutationAllowed(w, r) {
		return
	}
	a.retiredClippingRead(w, r)
}
