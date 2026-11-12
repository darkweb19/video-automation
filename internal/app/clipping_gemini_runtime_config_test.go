package app

import (
	"errors"
	"testing"
)

func TestLoadClippingGeminiRuntimeConfigIsOptIn(t *testing.T) {
	for _, enabled := range []string{"", "false", "TRUE", "1", "yes", "enabled"} {
		config, err := loadClippingGeminiRuntimeConfig(func(key string) string {
			if key == clippingGeminiEnabledEnv {
				return enabled
			}
			return "invalid-but-disabled"
		})
		if err != nil || config.Enabled || config.Routing != (ClippingGeminiConfig{}) {
			t.Errorf("enabled=%q returned config=%+v error=%v; want disabled zero config", enabled, config, err)
		}
	}
	if _, err := loadClippingGeminiRuntimeConfig(nil); !errors.Is(err, errClippingGeminiRuntimeConfig) {
		t.Fatalf("nil environment reader error=%v", err)
	}
}

func TestLoadClippingGeminiRuntimeConfigRequiresExplicitValidRouting(t *testing.T) {
	values := map[string]string{
		clippingGeminiEnabledEnv:  "true",
		clippingGeminiProjectEnv:  "framevault-test-123",
		clippingGeminiLocationEnv: "global",
	}
	getenv := func(key string) string { return values[key] }
	config, err := loadClippingGeminiRuntimeConfig(getenv)
	if err != nil || !config.Enabled || config.Routing != (ClippingGeminiConfig{ProjectID: "framevault-test-123", Location: "global"}) {
		t.Fatalf("valid opt-in config=%+v error=%v", config, err)
	}

	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "missing project", key: clippingGeminiProjectEnv, value: ""},
		{name: "invalid project", key: clippingGeminiProjectEnv, value: "FrameVault"},
		{name: "missing location", key: clippingGeminiLocationEnv, value: ""},
		{name: "invalid location", key: clippingGeminiLocationEnv, value: "https://attacker.example"},
		{name: "whitespace is not repaired", key: clippingGeminiProjectEnv, value: " framevault-test-123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values[tc.key] = tc.value
			defer func() {
				values[tc.key] = map[string]string{
					clippingGeminiEnabledEnv:  "true",
					clippingGeminiProjectEnv:  "framevault-test-123",
					clippingGeminiLocationEnv: "global",
				}[tc.key]
			}()
			config, err := loadClippingGeminiRuntimeConfig(getenv)
			if !errors.Is(err, errClippingGeminiRuntimeConfig) || config.Enabled {
				t.Fatalf("invalid opt-in config=%+v error=%v", config, err)
			}
		})
	}
}

func TestClippingGeminiRuntimeEnablementIsExact(t *testing.T) {
	for _, value := range []string{"true", "TRUE", "1", "false", ""} {
		if got := clippingGeminiRuntimeEnabled(value); got != (value == "true") {
			t.Errorf("clippingGeminiRuntimeEnabled(%q)=%t", value, got)
		}
	}
}
