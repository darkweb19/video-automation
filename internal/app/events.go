package app

import (
	"container/list"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (
	eventClientBuffer          = 32
	eventFingerprintCacheLimit = 256
	eventFrameWriteTimeout     = 15 * time.Second
)

type eventFingerprintKey struct {
	name string
	id   string
}

type eventFingerprint struct {
	hash    [sha256.Size]byte
	element *list.Element
}

// eventHub is intentionally live-only. SQLite remains the durable source of
// truth, so reconnecting clients refresh normal API resources rather than
// relying on an in-memory event replay buffer.
type eventHub struct {
	mu             sync.Mutex
	clients        map[chan dashboardEvent]struct{}
	fingerprints   map[eventFingerprintKey]*eventFingerprint
	fingerprintLRU *list.List
}

type dashboardEvent struct {
	name string
	data []byte
}

func newEventHub() *eventHub {
	return &eventHub{
		clients:        make(map[chan dashboardEvent]struct{}),
		fingerprints:   make(map[eventFingerprintKey]*eventFingerprint),
		fingerprintLRU: list.New(),
	}
}

func (h *eventHub) subscribe() (chan dashboardEvent, func()) {
	channel := make(chan dashboardEvent, eventClientBuffer)
	h.mu.Lock()
	if h.clients == nil {
		h.clients = make(map[chan dashboardEvent]struct{})
	}
	if len(h.clients) == 0 {
		// Do not carry fingerprints across disconnected periods. The browser
		// performs an authoritative API refresh after an EventSource reconnect.
		h.resetFingerprintsLocked()
	}
	h.clients[channel] = struct{}{}
	h.mu.Unlock()
	return channel, func() {
		h.mu.Lock()
		if _, exists := h.clients[channel]; exists {
			delete(h.clients, channel)
			close(channel)
			if len(h.clients) == 0 {
				h.resetFingerprintsLocked()
			}
		}
		h.mu.Unlock()
	}
}

func (h *eventHub) hasSubscribers() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) == 0 {
		h.resetFingerprintsLocked()
		return false
	}
	return true
}

func (h *eventHub) resetFingerprintsLocked() {
	// The common idle path calls this during every publish attempt. Preserve
	// already-empty structures (including nil ones) so there is no allocation
	// when no subscribers are connected.
	if len(h.fingerprints) > 0 {
		clear(h.fingerprints)
	}
	if h.fingerprintLRU != nil && h.fingerprintLRU.Len() > 0 {
		h.fingerprintLRU.Init()
	}
}

func (h *eventHub) removeFingerprintLocked(key eventFingerprintKey) {
	entry, exists := h.fingerprints[key]
	if !exists {
		return
	}
	delete(h.fingerprints, key)
	if entry.element != nil && h.fingerprintLRU != nil {
		h.fingerprintLRU.Remove(entry.element)
	}
}

func (h *eventHub) forgetFingerprint(name, id string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.removeFingerprintLocked(eventFingerprintKey{name: name, id: id})
	h.mu.Unlock()
}

func (h *eventHub) fingerprintMatchesLocked(key eventFingerprintKey, hash [sha256.Size]byte) bool {
	entry, exists := h.fingerprints[key]
	if !exists || entry.hash != hash {
		return false
	}
	if entry.element != nil && h.fingerprintLRU != nil {
		h.fingerprintLRU.MoveToFront(entry.element)
	}
	return true
}

func (h *eventHub) rememberFingerprintLocked(key eventFingerprintKey, hash [sha256.Size]byte) {
	if h.fingerprints == nil {
		h.fingerprints = make(map[eventFingerprintKey]*eventFingerprint)
	}
	if h.fingerprintLRU == nil {
		h.fingerprintLRU = list.New()
	}
	if entry, exists := h.fingerprints[key]; exists {
		entry.hash = hash
		h.fingerprintLRU.MoveToFront(entry.element)
		return
	}
	entry := &eventFingerprint{hash: hash}
	entry.element = h.fingerprintLRU.PushFront(key)
	h.fingerprints[key] = entry
	for len(h.fingerprints) > eventFingerprintCacheLimit {
		oldest := h.fingerprintLRU.Back()
		if oldest == nil {
			break
		}
		oldestKey := oldest.Value.(eventFingerprintKey)
		h.removeFingerprintLocked(oldestKey)
	}
}

func (h *eventHub) removeClientLocked(channel chan dashboardEvent) {
	if _, exists := h.clients[channel]; !exists {
		return
	}
	delete(h.clients, channel)
	// A slow client must reconnect and refresh from SQLite instead of receiving
	// a queue full of stale snapshots before learning about the latest state.
	for {
		select {
		case <-channel:
		default:
			close(channel)
			if len(h.clients) == 0 {
				h.resetFingerprintsLocked()
			}
			return
		}
	}
}

// publishSnapshot marshals only when there is a subscriber and the semantic
// snapshot changed. The cache retains bounded SHA-256 fingerprints, never the
// prompt, trace, or serialized event body.
func (h *eventHub) publishSnapshot(name, id string, fingerprint [sha256.Size]byte, payload any) {
	if h == nil {
		return
	}
	key := eventFingerprintKey{name: name, id: id}
	h.mu.Lock()
	if len(h.clients) == 0 {
		h.resetFingerprintsLocked()
		h.mu.Unlock()
		return
	}
	if h.fingerprintMatchesLocked(key, fingerprint) {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()

	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	event := dashboardEvent{name: name, data: encoded}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) == 0 {
		h.resetFingerprintsLocked()
		return
	}
	// A concurrent publisher may have sent the same snapshot while this body
	// was being marshaled.
	if h.fingerprintMatchesLocked(key, fingerprint) {
		return
	}
	accepted := false
	for channel := range h.clients {
		select {
		case channel <- event:
			accepted = true
		default:
			h.removeClientLocked(channel)
		}
	}
	if accepted && len(h.clients) > 0 {
		h.rememberFingerprintLocked(key, fingerprint)
	}
}

func generationSnapshotFingerprint(record GenerationRecord) ([sha256.Size]byte, error) {
	record.UpdatedAt = 0
	return snapshotFingerprint(record)
}

func projectSnapshotFingerprint(project VideoProject) ([sha256.Size]byte, error) {
	project.UpdatedAt = 0
	project.Scenes = append([]ProjectScene(nil), project.Scenes...)
	for index := range project.Scenes {
		project.Scenes[index].UpdatedAt = 0
	}
	return snapshotFingerprint(project)
}

func snapshotFingerprint(snapshot any) ([sha256.Size]byte, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func (s *Store) PublishGeneration(id string) {
	if s.events == nil || !safeID(id) || !s.events.hasSubscribers() {
		return
	}
	record, err := s.Generation(id)
	if err != nil {
		return
	}
	if record.InVault {
		s.events.forgetFingerprint("generation", id)
		return
	}
	fingerprint, err := generationSnapshotFingerprint(record)
	if err == nil {
		s.events.publishSnapshot("generation", id, fingerprint, map[string]any{"id": id, "generation": record})
	}
}

func (s *Store) PublishProject(id string) {
	if s.events == nil || !safeID(id) || !s.events.hasSubscribers() {
		return
	}
	project, err := s.projectLiveSnapshot(id)
	if err != nil {
		return
	}
	if project.InVault {
		s.events.forgetFingerprint("project", id)
		return
	}
	fingerprint, err := projectSnapshotFingerprint(project)
	if err == nil {
		s.events.publishSnapshot("project", id, fingerprint, map[string]any{"id": id, "project": project})
	}
}

// writeEventFrame gives each individual SSE write and flush a bounded deadline
// while leaving the connection itself long-lived. Without this, a client that
// stops reading can leave a handler blocked in Flush even after the hub drops
// its full event queue. A ResponseController reaches the error-returning
// FlushError method provided by Go's HTTP/1 and HTTP/2 response writers.
func writeEventFrame(w http.ResponseWriter, controller *http.ResponseController, frame string) error {
	if err := controller.SetWriteDeadline(time.Now().Add(eventFrameWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	defer func() {
		// Do not turn the per-frame deadline into a connection lifetime. This
		// reset also preserves healthy SSE streams beyond the server's normal
		// API-response write timeout.
		_ = controller.SetWriteDeadline(time.Time{})
	}()
	if _, err := fmt.Fprint(w, frame); err != nil {
		return err
	}
	return controller.Flush()
}

func (a *dashboardApp) events(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		writeError(w, http.StatusInternalServerError, "event streaming is unavailable")
		return
	}
	if a.store.events == nil {
		writeError(w, http.StatusServiceUnavailable, "event streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	channel, unsubscribe := a.store.events.subscribe()
	defer unsubscribe()
	if err := writeEventFrame(w, controller, "retry: 3000\n\nevent: ready\ndata: {\"retry_ms\":3000}\n\n"); err != nil {
		return
	}
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-channel:
			if !open {
				return
			}
			if err := writeEventFrame(w, controller, fmt.Sprintf("event: %s\ndata: %s\n\n", event.name, event.data)); err != nil {
				return
			}
		case <-heartbeat.C:
			if a.security != nil {
				identity, ok := a.security.Session(r)
				if !ok || identity.MustChangePassword {
					return
				}
			}
			if err := writeEventFrame(w, controller, "event: ping\ndata: {}\n\n"); err != nil {
				return
			}
		}
	}
}
