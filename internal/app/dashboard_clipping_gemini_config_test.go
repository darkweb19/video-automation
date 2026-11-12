package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClippingGeminiDescriptorReportsOnlyLocalConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name                string
		env                 map[string]string
		configured, enabled bool
	}{
		{"missing", nil, false, false},
		{"disabled but configured", map[string]string{clippingGeminiProjectEnv: "project-secret-name", clippingGeminiLocationEnv: "global"}, true, false},
		{"enabled and configured", map[string]string{clippingGeminiEnabledEnv: "true", clippingGeminiProjectEnv: "project-secret-name", clippingGeminiLocationEnv: "global"}, true, true},
		{"enabled with invalid config", map[string]string{clippingGeminiEnabledEnv: "true", clippingGeminiProjectEnv: "project-secret-name", clippingGeminiLocationEnv: "https://attacker.example"}, false, true},
		{"opt in is exact", map[string]string{clippingGeminiEnabledEnv: "TRUE", clippingGeminiProjectEnv: "project-secret-name", clippingGeminiLocationEnv: "global"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := clippingGeminiDescriptor(func(key string) string { return tc.env[key] })
			if view.Configured != tc.configured || view.Enabled != tc.enabled || view.Verification != "unverified" {
				t.Fatalf("descriptor = %+v", view)
			}
			if view.Limits.MinClipMS != ClippingGeminiMinClipMS || view.Limits.MaxClipMS != ClippingGeminiMaxClipMS || view.Limits.MaxCandidates != ClippingGeminiMaxCandidates || view.Limits.MaxSourceDurationMS != clippingGeminiMaxRequestDurationMS {
				t.Fatalf("incorrect limits: %+v", view.Limits)
			}
			encoded, err := json.Marshal(view)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"project-secret-name", "global", "attacker.example", "credential", "model"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Errorf("descriptor exposes %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestClippingGeminiDescriptorNilEnvironmentIsInert(t *testing.T) {
	view := clippingGeminiDescriptor(nil)
	if view.Configured || view.Enabled || view.Verification != "unverified" {
		t.Fatalf("descriptor = %+v", view)
	}
}
