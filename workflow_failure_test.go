package main

import (
	"errors"
	"strings"
	"testing"
)

func TestSafeStoryPlanSaveFailure(t *testing.T) {
	message := safeStoryPlanSaveFailure(errors.New("scene 3 prompt exceeds 4000 characters after continuity rules"))
	if !strings.Contains(message, "4,000-character limit") {
		t.Fatalf("expected prompt-limit guidance, got %q", message)
	}
	message = safeStoryPlanSaveFailure(errors.New("database error: secret value"))
	if strings.Contains(message, "secret value") || !strings.Contains(message, "Retry the project") {
		t.Fatalf("unexpected internal error exposure: %q", message)
	}
}
