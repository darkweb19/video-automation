package app

import "testing"

func TestClippingGeminiLifecycleMigrationCreatesTypedAttemptTables(t *testing.T) {
	store := newTestStore(t)
	for _, table := range []string{"clipping_gemini_attempts", "clipping_gemini_execution_claims"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("lifecycle table %s count=%d error=%v", table, count, err)
		}
	}
	var fields int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('clipping_gemini_attempts') WHERE name IN ('usage_available','model_version','retry_at')`).Scan(&fields); err != nil || fields != 3 {
		t.Fatalf("attempt typed fields count=%d error=%v", fields, err)
	}
}
