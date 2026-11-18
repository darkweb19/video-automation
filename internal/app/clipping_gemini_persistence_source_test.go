package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCreateClippingYouTubeReferenceProtectsURLWithoutMedia(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.CreateClippingYouTubeReference(security, "https://youtu.be/abcdefghijk?t=2", true, time.Now().Add(time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != ClippingSourceYouTubeURL || source.Status != ClippingSourceReady || !source.RightsAttested || source.StoragePath != "" || source.SHA256 != "" || source.SizeBytes != 0 || source.DurationMS != 0 || source.ReservedSizeBytes != 0 {
		t.Fatalf("reference acquired fabricated file metadata: %+v", source)
	}
	loaded, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := security.DecryptSetting(clippingSourceURLSetting, loaded.SourceURL)
	if err != nil || plaintext != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("canonical encrypted URL unavailable: %v", err)
	}
	rows, err := store.db.Query(`SELECT * FROM clipping_sources WHERE id=?`, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(values))
	for i := range values {
		pointers[i] = &values[i]
	}
	if !rows.Next() {
		t.Fatal("missing stored source")
	}
	if err := rows.Scan(pointers...); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for i, value := range values {
		var text string
		switch v := value.(type) {
		case string:
			text = v
		case []byte:
			text = string(v)
		}
		if strings.Contains(text, "abcdefghijk") || strings.Contains(text, plaintext) {
			t.Errorf("source column %s reveals URL or video ID", columns[i])
		}
	}
	encoded, err := json.Marshal(loaded)
	if err != nil || strings.Contains(string(encoded), loaded.SourceURL) || strings.Contains(string(encoded), plaintext) || strings.Contains(string(encoded), "abcdefghijk") {
		t.Fatalf("serialized source exposed protected URL: %v", err)
	}
	entries, err := os.ReadDir(store.clippingDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("reference created local media or directory: %v error=%v", entries, err)
	}
}

func TestCreateClippingYouTubeReferenceRejectsUnsafeInput(t *testing.T) {
	store := newTestStore(t)
	security, err := NewSecurity(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		url    string
		rights bool
		retain int64
	}{
		{"https://youtu.be/abcdefghijk", false, 0},
		{"http://youtube.com/watch?v=abcdefghijk", true, 0},
		{"https://youtube.com/watch?v=abcdefghijk&list=playlist", true, 0},
		{"https://evil.example/watch?v=abcdefghijk", true, 0},
		{"https://youtu.be/abcdefghijk", true, -1},
		{"https://youtu.be/abcdefghijk", true, time.Now().Add(-time.Second).Unix()},
	} {
		if _, err := store.CreateClippingYouTubeReference(security, input.url, input.rights, input.retain); err == nil {
			t.Fatalf("accepted invalid reference input: %+v", input)
		}
	}
	if _, err := store.CreateClippingYouTubeReference(nil, "https://youtu.be/abcdefghijk", true, 0); err == nil {
		t.Fatal("accepted reference without encryption")
	}
	if _, err := store.CreateClippingSource(ClippingSourceCreate{Kind: ClippingSourceYouTubeURL, RightsAttested: true, DeclaredSizeBytes: 1, SourceURL: "plaintext"}); err == nil {
		t.Fatal("generic file creator accepted an unprotected URL reference")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM clipping_sources`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid references created rows: %d error=%v", count, err)
	}
}
