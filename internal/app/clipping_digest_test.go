package app

import (
	"strings"
	"testing"
)

func TestValidLowerHexDigest(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		valid bool
	}{
		{"lowercase hex", strings.Repeat("0f", 32), true},
		{"empty", "", false},
		{"too short", strings.Repeat("a", 63), false},
		{"too long", strings.Repeat("a", 65), false},
		{"uppercase", strings.Repeat("A", 64), false},
		{"non hex", strings.Repeat("f", 63) + "g", false},
		{"non ASCII", strings.Repeat("a", 62) + "é", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validLowerHexDigest(test.value); got != test.valid {
				t.Fatalf("validLowerHexDigest() = %v, want %v", got, test.valid)
			}
		})
	}
}
