package config

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDefaultWrapConfig(t *testing.T) {
	cfg := DefaultWrapConfig()

	if cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", cfg.MaxRetries)
	}
	if cfg.InitialDelay.Duration() != 30*time.Second {
		t.Errorf("InitialDelay = %v, want 30s", cfg.InitialDelay.Duration())
	}
	if cfg.MaxDelay.Duration() != 5*time.Minute {
		t.Errorf("MaxDelay = %v, want 5m", cfg.MaxDelay.Duration())
	}
	if cfg.BackoffMultiplier != 2.0 {
		t.Errorf("BackoffMultiplier = %f, want 2.0", cfg.BackoffMultiplier)
	}
	if !cfg.Jitter {
		t.Errorf("Jitter = false, want true")
	}
	if cfg.CooldownDuration.Duration() != 60*time.Minute {
		t.Errorf("CooldownDuration = %v, want 60m", cfg.CooldownDuration.Duration())
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}
}

func TestWrapConfig_NextDelay_ExponentialBackoff(t *testing.T) {
	cfg := WrapConfig{
		InitialDelay:      Duration(10 * time.Second),
		MaxDelay:          Duration(5 * time.Minute),
		BackoffMultiplier: 2.0,
		Jitter:            false, // Disable jitter for predictable tests
	}

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 10 * time.Second},  // 10 * 2^0 = 10s
		{1, 20 * time.Second},  // 10 * 2^1 = 20s
		{2, 40 * time.Second},  // 10 * 2^2 = 40s
		{3, 80 * time.Second},  // 10 * 2^3 = 80s
		{4, 160 * time.Second}, // 10 * 2^4 = 160s
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := cfg.NextDelay(tt.attempt)
			if got != tt.expected {
				t.Errorf("NextDelay(%d) = %v, want %v", tt.attempt, got, tt.expected)
			}
		})
	}
}

func TestWrapConfig_NextDelay_MaxDelayCap(t *testing.T) {
	cfg := WrapConfig{
		InitialDelay:      Duration(30 * time.Second),
		MaxDelay:          Duration(2 * time.Minute), // 120s max
		BackoffMultiplier: 2.0,
		Jitter:            false,
	}

	// After 3 attempts: 30 * 2^3 = 240s, which exceeds max of 120s
	got := cfg.NextDelay(3)
	if got != 2*time.Minute {
		t.Errorf("NextDelay(3) = %v, want 2m (capped at max)", got)
	}
}

func TestWrapConfig_NextDelay_Jitter(t *testing.T) {
	cfg := WrapConfig{
		InitialDelay:      Duration(100 * time.Second),
		MaxDelay:          Duration(5 * time.Minute),
		BackoffMultiplier: 2.0,
		Jitter:            true,
	}

	// With jitter enabled, delays should vary by ±20%
	// 100s * 0.8 = 80s minimum
	// 100s * 1.2 = 120s maximum
	minExpected := 80 * time.Second
	maxExpected := 120 * time.Second

	// Run multiple times to test jitter range
	for i := 0; i < 10; i++ {
		got := cfg.NextDelay(0)
		if got < minExpected || got > maxExpected {
			t.Errorf("NextDelay(0) with jitter = %v, want between %v and %v", got, minExpected, maxExpected)
		}
	}
}

func TestWrapConfig_NextDelay_NegativeAttempt(t *testing.T) {
	cfg := WrapConfig{
		InitialDelay:      Duration(10 * time.Second),
		MaxDelay:          Duration(5 * time.Minute),
		BackoffMultiplier: 2.0,
		Jitter:            false,
	}

	// Negative attempt should be treated as 0
	got := cfg.NextDelay(-1)
	if got != 10*time.Second {
		t.Errorf("NextDelay(-1) = %v, want 10s", got)
	}
}

func TestWrapConfig_NextDelay_ZeroMultiplier(t *testing.T) {
	cfg := WrapConfig{
		InitialDelay:      Duration(10 * time.Second),
		MaxDelay:          Duration(5 * time.Minute),
		BackoffMultiplier: 0, // Invalid, should default to 2.0
		Jitter:            false,
	}

	// Should default to 2.0 multiplier
	got := cfg.NextDelay(1)
	if got != 20*time.Second {
		t.Errorf("NextDelay(1) with zero multiplier = %v, want 20s (default multiplier 2.0)", got)
	}
}

func TestWrapConfig_NextDelay_ExtremeValues(t *testing.T) {
	const largestDuration = time.Duration(1<<63 - 1)
	tests := []struct {
		name       string
		initial    time.Duration
		maximum    time.Duration
		multiplier float64
		attempt    int
		want       time.Duration
	}{
		{"largest duration", largestDuration, largestDuration, 2, 0, largestDuration},
		{"exponent overflow", time.Second, largestDuration, 2, 1024, largestDuration},
		{"multiplication overflow", 2 * time.Second, time.Hour, math.MaxFloat64, 2, time.Hour},
		{"zero initial with huge attempt", 0, time.Minute, 2, int(^uint(0) >> 1), 0},
		{"zero maximum", 0, 0, 2, 1, 0},
		{"decreasing backoff", 10 * time.Second, time.Minute, 0.5, 3, 1250 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := WrapConfig{
				InitialDelay:      Duration(tt.initial),
				MaxDelay:          Duration(tt.maximum),
				BackoffMultiplier: tt.multiplier,
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("test config is invalid: %v", err)
			}
			if got := cfg.NextDelay(tt.attempt); got != tt.want {
				t.Fatalf("NextDelay(%d) = %v, want %v", tt.attempt, got, tt.want)
			}
		})
	}
}

func TestWrapConfig_NextDelay_JitterNeverExceedsCap(t *testing.T) {
	for _, maximum := range []time.Duration{time.Second, time.Duration(1<<63 - 1)} {
		cfg := WrapConfig{
			InitialDelay:      Duration(maximum),
			MaxDelay:          Duration(maximum),
			BackoffMultiplier: 2,
			Jitter:            true,
		}
		for i := 0; i < 128; i++ {
			got := cfg.NextDelay(1024)
			if got < 0 || got > maximum {
				t.Fatalf("jittered delay = %v, want 0 <= delay <= %v", got, maximum)
			}
		}
	}
}

func TestWrapConfig_ShouldRetry(t *testing.T) {
	cfg := WrapConfig{MaxRetries: 3}

	tests := []struct {
		attempt  int
		expected bool
	}{
		{0, true},
		{1, true},
		{2, true},
		{3, false}, // Reached max
		{4, false},
	}

	for _, tt := range tests {
		got := cfg.ShouldRetry(tt.attempt)
		if got != tt.expected {
			t.Errorf("ShouldRetry(%d) = %v, want %v", tt.attempt, got, tt.expected)
		}
	}
}

func TestWrapConfig_ForProvider_NoOverrides(t *testing.T) {
	cfg := WrapConfig{
		MaxRetries:        3,
		InitialDelay:      Duration(30 * time.Second),
		BackoffMultiplier: 2.0,
		Providers:         nil, // No provider overrides
	}

	result := cfg.ForProvider("claude")

	if result.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", result.MaxRetries)
	}
	if result.InitialDelay.Duration() != 30*time.Second {
		t.Errorf("InitialDelay = %v, want 30s", result.InitialDelay.Duration())
	}
}

func TestWrapConfig_ForProvider_WithOverrides(t *testing.T) {
	maxRetries := 5
	initialDelay := Duration(60 * time.Second)
	cfg := WrapConfig{
		MaxRetries:        3,
		InitialDelay:      Duration(30 * time.Second),
		MaxDelay:          Duration(5 * time.Minute),
		BackoffMultiplier: 2.0,
		Jitter:            true,
		CooldownDuration:  Duration(60 * time.Minute),
		Providers: map[string]*WrapOverride{
			"claude": {
				MaxRetries:   &maxRetries,
				InitialDelay: &initialDelay,
			},
		},
	}

	result := cfg.ForProvider("claude")

	// Overridden values
	if result.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, want 5", result.MaxRetries)
	}
	if result.InitialDelay.Duration() != 60*time.Second {
		t.Errorf("InitialDelay = %v, want 60s", result.InitialDelay.Duration())
	}

	// Non-overridden values should remain from base
	if result.MaxDelay.Duration() != 5*time.Minute {
		t.Errorf("MaxDelay = %v, want 5m", result.MaxDelay.Duration())
	}
	if result.BackoffMultiplier != 2.0 {
		t.Errorf("BackoffMultiplier = %f, want 2.0", result.BackoffMultiplier)
	}
	if !result.Jitter {
		t.Error("omitted jitter override disabled inherited jitter")
	}
}

func TestWrapConfig_ForProvider_UnknownProvider(t *testing.T) {
	maxRetries := 5
	cfg := WrapConfig{
		MaxRetries:   3,
		InitialDelay: Duration(30 * time.Second),
		Providers: map[string]*WrapOverride{
			"claude": {MaxRetries: &maxRetries},
		},
	}

	// Unknown provider should return base config
	result := cfg.ForProvider("unknown")

	if result.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", result.MaxRetries)
	}
}

func TestWrapConfig_ForProvider_ExplicitJSONOverrides(t *testing.T) {
	defaults := DefaultWrapConfig()
	tests := []struct {
		name     string
		override string
		change   func(*WrapConfig)
	}{
		{"empty inherits", `{}`, func(*WrapConfig) {}},
		{"null inherits", `null`, func(*WrapConfig) {}},
		{"no retries", `{"max_retries":0}`, func(c *WrapConfig) { c.MaxRetries = 0 }},
		{"immediate retry", `{"initial_delay":"0s"}`, func(c *WrapConfig) { c.InitialDelay = 0 }},
		{"disable jitter", `{"jitter":false}`, func(c *WrapConfig) { c.Jitter = false }},
		{"no cooldown", `{"cooldown_duration":"0s"}`, func(c *WrapConfig) { c.CooldownDuration = 0 }},
		{"zero delays", `{"initial_delay":"0s","max_delay":"0s"}`, func(c *WrapConfig) {
			c.InitialDelay = 0
			c.MaxDelay = 0
		}},
		{"partial override inherits jitter", `{"max_retries":7}`, func(c *WrapConfig) { c.MaxRetries = 7 }},
		{"delay and multiplier", `{"max_delay":"1m","backoff_multiplier":1.5}`, func(c *WrapConfig) {
			c.MaxDelay = Duration(time.Minute)
			c.BackoffMultiplier = 1.5
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaults
			if err := json.Unmarshal([]byte(`{"providers":{"claude":`+tt.override+`}}`), &cfg); err != nil {
				t.Fatalf("decode: %v", err)
			}
			want := defaults
			tt.change(&want)
			got := cfg.ForProvider("claude")
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("effective config = %+v, want %+v", got, want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("effective config is invalid: %v", err)
			}
			if base := cfg.ForProvider("codex"); !reflect.DeepEqual(base, defaults) {
				t.Fatalf("provider override changed base settings: %+v", base)
			}
		})
	}
}

func TestWrapConfig_Validate(t *testing.T) {
	tests := []struct {
		name   string
		change func(*WrapConfig)
		field  string
	}{
		{"negative retries", func(c *WrapConfig) { c.MaxRetries = -1 }, "max_retries"},
		{"negative initial delay", func(c *WrapConfig) { c.InitialDelay = -1 }, "initial_delay"},
		{"negative maximum delay", func(c *WrapConfig) { c.MaxDelay = -1 }, "max_delay"},
		{"negative cooldown", func(c *WrapConfig) { c.CooldownDuration = -1 }, "cooldown_duration"},
		{"zero multiplier", func(c *WrapConfig) { c.BackoffMultiplier = 0 }, "backoff_multiplier"},
		{"negative multiplier", func(c *WrapConfig) { c.BackoffMultiplier = -1 }, "backoff_multiplier"},
		{"NaN multiplier", func(c *WrapConfig) { c.BackoffMultiplier = math.NaN() }, "backoff_multiplier"},
		{"infinite multiplier", func(c *WrapConfig) { c.BackoffMultiplier = math.Inf(1) }, "backoff_multiplier"},
		{"negative infinite multiplier", func(c *WrapConfig) { c.BackoffMultiplier = math.Inf(-1) }, "backoff_multiplier"},
		{"initial above maximum", func(c *WrapConfig) { c.InitialDelay = c.MaxDelay + 1 }, "initial_delay"},
		{"zero maximum with positive initial", func(c *WrapConfig) { c.MaxDelay = 0 }, "max_delay"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultWrapConfig()
			tt.change(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("Validate() = %v, want error identifying %s", err, tt.field)
			}
		})
	}
}

func TestWrapConfig_InvalidProviderOverrideIsNotIgnored(t *testing.T) {
	for _, override := range []string{
		`{"max_retries":-1}`,
		`{"initial_delay":"-1s"}`,
		`{"max_delay":"-1s"}`,
		`{"cooldown_duration":"-1s"}`,
		`{"backoff_multiplier":0}`,
		`{"initial_delay":"6m"}`,
	} {
		cfg := DefaultWrapConfig()
		if err := json.Unmarshal([]byte(`{"providers":{"claude":`+override+`}}`), &cfg); err != nil {
			t.Fatalf("decode %s: %v", override, err)
		}
		got := cfg.ForProvider("claude")
		if err := got.Validate(); err == nil {
			t.Errorf("invalid override %s silently inherited valid defaults", override)
		}
	}
}

func TestDuration_JSON(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected time.Duration
	}{
		{"30 seconds", `"30s"`, 30 * time.Second},
		{"5 minutes", `"5m"`, 5 * time.Minute},
		{"1 hour", `"1h"`, 1 * time.Hour},
		{"combined", `"1h30m"`, 90 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			if err := json.Unmarshal([]byte(tt.json), &d); err != nil {
				t.Fatalf("Unmarshal error: %v", err)
			}
			if d.Duration() != tt.expected {
				t.Errorf("Duration = %v, want %v", d.Duration(), tt.expected)
			}
		})
	}
}

func TestDuration_JSON_Roundtrip(t *testing.T) {
	original := Duration(2*time.Hour + 30*time.Minute)

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded Duration
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decoded.Duration() != original.Duration() {
		t.Errorf("Roundtrip: got %v, want %v", decoded.Duration(), original.Duration())
	}
}

func TestWrapConfig_JSON_Roundtrip(t *testing.T) {
	maxRetries := 10
	original := WrapConfig{
		MaxRetries:        5,
		InitialDelay:      Duration(45 * time.Second),
		MaxDelay:          Duration(10 * time.Minute),
		BackoffMultiplier: 1.5,
		Jitter:            true,
		CooldownDuration:  Duration(30 * time.Minute),
		Providers: map[string]*WrapOverride{
			"claude": {MaxRetries: &maxRetries},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded WrapConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decoded.MaxRetries != original.MaxRetries {
		t.Errorf("MaxRetries = %d, want %d", decoded.MaxRetries, original.MaxRetries)
	}
	if decoded.InitialDelay.Duration() != original.InitialDelay.Duration() {
		t.Errorf("InitialDelay = %v, want %v", decoded.InitialDelay.Duration(), original.InitialDelay.Duration())
	}
	if decoded.BackoffMultiplier != original.BackoffMultiplier {
		t.Errorf("BackoffMultiplier = %f, want %f", decoded.BackoffMultiplier, original.BackoffMultiplier)
	}
	if got := decoded.Providers["claude"].MaxRetries; got == nil || *got != 10 {
		t.Errorf("Providers[claude].MaxRetries = %v, want pointer to 10", got)
	}
}

func TestWrapOverride_JSONPreservesOmissionAndZero(t *testing.T) {
	const input = `{"max_retries":0,"initial_delay":"0s","jitter":false}`
	var override WrapOverride
	if err := json.Unmarshal([]byte(input), &override); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(override)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != input {
		t.Fatalf("override round trip = %s, want %s", encoded, input)
	}
}
