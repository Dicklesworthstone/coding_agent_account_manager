package prediction

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/logs"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// mockScanner implements logs.Scanner for testing.
type mockScanner struct {
	entries []*logs.LogEntry
	err     error
}

func (m *mockScanner) Scan(ctx context.Context, logDir string, since time.Time) (*logs.ScanResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &logs.ScanResult{
		Provider:      "mock",
		TotalEntries:  len(m.entries),
		ParsedEntries: len(m.entries),
		Entries:       m.entries,
	}, nil
}

func (m *mockScanner) LogDir() string {
	return "/mock/logs"
}

func TestNewPredictionEngine(t *testing.T) {
	e := NewPredictionEngine()
	if e == nil {
		t.Fatal("NewPredictionEngine() returned nil")
	}
	if e.logWindow != 2*time.Hour {
		t.Errorf("logWindow = %v, want 2h", e.logWindow)
	}
	if e.sessionWindow != 30*time.Minute {
		t.Errorf("sessionWindow = %v, want 30m", e.sessionWindow)
	}
}

func TestNewPredictionEngine_WithOptions(t *testing.T) {
	scanner := &mockScanner{}
	tracker := usage.NewSessionTracker()

	e := NewPredictionEngine(
		WithLogScanner(scanner),
		WithSessionTracker(tracker),
		WithLogWindow(1*time.Hour),
		WithSessionWindow(15*time.Minute),
		WithModel("sonnet"),
	)

	if e.logScanner != scanner {
		t.Error("logScanner not set")
	}
	if e.sessionTracker != tracker {
		t.Error("sessionTracker not set")
	}
	if e.logWindow != 1*time.Hour {
		t.Errorf("logWindow = %v, want 1h", e.logWindow)
	}
	if e.sessionWindow != 15*time.Minute {
		t.Errorf("sessionWindow = %v, want 15m", e.sessionWindow)
	}
	if e.model != "sonnet" {
		t.Errorf("model = %q, want sonnet", e.model)
	}
}

func TestPredictionEngine_Predict_NoUsageInfo(t *testing.T) {
	e := NewPredictionEngine()
	pred := e.Predict(context.Background(), nil)

	if pred.Error == "" {
		t.Error("Expected error for nil usageInfo")
	}
}

func TestPredictionEngine_Predict_NoWindow(t *testing.T) {
	e := NewPredictionEngine()
	usageInfo := &usage.UsageInfo{
		Provider:    "claude",
		ProfileName: "test",
	}

	pred := e.Predict(context.Background(), usageInfo)

	if pred.Error == "" {
		t.Error("Expected error for missing window")
	}
}

func TestPredictionEngine_Predict_AlreadyDepleted(t *testing.T) {
	e := NewPredictionEngine()
	usageInfo := &usage.UsageInfo{
		Provider:    "claude",
		ProfileName: "test",
		PrimaryWindow: &usage.UsageWindow{
			UsedPercent: 100,
		},
	}

	pred := e.Predict(context.Background(), usageInfo)

	if pred.Warning != WarningImminent {
		t.Errorf("Warning = %v, want WarningImminent", pred.Warning)
	}
	if pred.CurrentPercent != 100 {
		t.Errorf("CurrentPercent = %v, want 100", pred.CurrentPercent)
	}
	// Already depleted predictions should be valid
	if !pred.IsValid() {
		t.Errorf("IsValid() = false, want true for depleted prediction")
	}
	if pred.Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0 for certain depletion", pred.Confidence)
	}
	if pred.TimeToDepletion != 0 {
		t.Errorf("TimeToDepletion = %v, want 0", pred.TimeToDepletion)
	}
}

func TestPredictionEngine_Predict_WithSessionData(t *testing.T) {
	tracker := usage.NewSessionTracker()

	// Add session entries spanning 15 minutes with sufficient time gaps
	now := time.Now()
	for i := 0; i < 10; i++ {
		tracker.Record(usage.TokenEntry{
			Timestamp:    now.Add(-time.Duration(i*2) * time.Minute), // 2 min gaps
			Model:        "claude-3-opus",
			InputTokens:  1000,
			OutputTokens: 2000,
			Source:       "api_response",
		})
	}

	e := NewPredictionEngine(
		WithSessionTracker(tracker),
		WithSessionWindow(30*time.Minute),
	)

	usageInfo := &usage.UsageInfo{
		Provider:    "claude",
		ProfileName: "test",
		PrimaryWindow: &usage.UsageWindow{
			UsedPercent: 50,
			ResetsAt:    time.Now().Add(8 * time.Hour),
		},
		// Session data won't have PercentPerHour without TokenLimit,
		// so prediction should fall back to API burn rate
		BurnRate: &usage.BurnRateInfo{
			PercentPerHour: 10,
			Confidence:     0.8,
			SampleSize:     10,
		},
	}

	pred := e.Predict(context.Background(), usageInfo)

	if pred.Error != "" {
		t.Fatalf("Unexpected error: %s", pred.Error)
	}

	// Falls back to API since session data lacks PercentPerHour
	hasAPI := false
	for _, src := range pred.DataSources {
		if src == "api" {
			hasAPI = true
			break
		}
	}
	if !hasAPI {
		t.Errorf("Expected 'api' in DataSources, got %v", pred.DataSources)
	}

	if pred.CurrentPercent != 50 {
		t.Errorf("CurrentPercent = %v, want 50", pred.CurrentPercent)
	}
}

func TestPredictionEngine_Predict_WithLogData(t *testing.T) {
	now := time.Now()
	scanner := &mockScanner{
		entries: []*logs.LogEntry{
			{Timestamp: now.Add(-50 * time.Minute), Model: "claude-3-opus", InputTokens: 1000, OutputTokens: 2000, TotalTokens: 3000},
			{Timestamp: now.Add(-40 * time.Minute), Model: "claude-3-opus", InputTokens: 1500, OutputTokens: 2500, TotalTokens: 4000},
			{Timestamp: now.Add(-30 * time.Minute), Model: "claude-3-opus", InputTokens: 1200, OutputTokens: 2800, TotalTokens: 4000},
			{Timestamp: now.Add(-20 * time.Minute), Model: "claude-3-opus", InputTokens: 1000, OutputTokens: 2000, TotalTokens: 3000},
			{Timestamp: now.Add(-10 * time.Minute), Model: "claude-3-opus", InputTokens: 1500, OutputTokens: 2500, TotalTokens: 4000},
		},
	}

	e := NewPredictionEngine(
		WithLogScanner(scanner),
		WithLogWindow(1*time.Hour),
	)

	usageInfo := &usage.UsageInfo{
		Provider:    "claude",
		ProfileName: "test",
		PrimaryWindow: &usage.UsageWindow{
			UsedPercent: 60,
			ResetsAt:    time.Now().Add(3 * time.Hour),
		},
		// Log data won't have PercentPerHour without TokenLimit,
		// so prediction should fall back to API burn rate
		BurnRate: &usage.BurnRateInfo{
			PercentPerHour: 15,
			Confidence:     0.7,
			SampleSize:     10,
		},
	}

	pred := e.Predict(context.Background(), usageInfo)

	if pred.Error != "" {
		t.Fatalf("Unexpected error: %s", pred.Error)
	}

	// Falls back to API since log data lacks PercentPerHour
	hasAPI := false
	for _, src := range pred.DataSources {
		if src == "api" {
			hasAPI = true
			break
		}
	}
	if !hasAPI {
		t.Errorf("Expected 'api' in DataSources, got %v", pred.DataSources)
	}
}

func TestPredictionEngine_Predict_WithAPIBurnRate(t *testing.T) {
	e := NewPredictionEngine() // No scanner or tracker

	usageInfo := &usage.UsageInfo{
		Provider:    "claude",
		ProfileName: "test",
		PrimaryWindow: &usage.UsageWindow{
			UsedPercent: 40,
			ResetsAt:    time.Now().Add(5 * time.Hour),
		},
		BurnRate: &usage.BurnRateInfo{
			PercentPerHour: 20,
			Confidence:     0.6,
			SampleSize:     10,
		},
	}

	pred := e.Predict(context.Background(), usageInfo)

	if pred.Error != "" {
		t.Fatalf("Unexpected error: %s", pred.Error)
	}

	// Should have api as data source
	hasAPI := false
	for _, src := range pred.DataSources {
		if src == "api" {
			hasAPI = true
			break
		}
	}
	if !hasAPI {
		t.Error("Expected 'api' in DataSources")
	}

	// 60% remaining at 20%/hour = 3 hours
	expectedHours := 3.0
	actualHours := pred.TimeToDepletion.Hours()
	if actualHours < expectedHours-0.1 || actualHours > expectedHours+0.1 {
		t.Errorf("TimeToDepletion = %v hours, want ~%v hours", actualHours, expectedHours)
	}
}

func TestPredictionEngine_Predict_WarningLevels(t *testing.T) {
	tests := []struct {
		name           string
		percentPerHour float64
		usedPercent    int
		wantWarning    WarningLevel
	}{
		{
			name:           "imminent (< 10 min)",
			percentPerHour: 600, // 10% per minute = 100% in 10 min
			usedPercent:    95,  // 5% remaining = 0.5 min
			wantWarning:    WarningImminent,
		},
		{
			name:           "approaching (< 30 min)",
			percentPerHour: 200,             // ~3.3% per minute
			usedPercent:    90,              // 10% remaining = ~3 min
			wantWarning:    WarningImminent, // Actually this is < 10 min
		},
		{
			name:           "approaching (10-30 min)",
			percentPerHour: 50, // 0.83% per minute
			usedPercent:    80, // 20% remaining = 24 min
			wantWarning:    WarningApproaching,
		},
		{
			name:           "none (> 30 min)",
			percentPerHour: 10,
			usedPercent:    50, // 50% remaining = 5 hours
			wantWarning:    WarningNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewPredictionEngine()
			usageInfo := &usage.UsageInfo{
				Provider:    "claude",
				ProfileName: "test",
				PrimaryWindow: &usage.UsageWindow{
					UsedPercent: tt.usedPercent,
					ResetsAt:    time.Now().Add(10 * time.Hour),
				},
				BurnRate: &usage.BurnRateInfo{
					PercentPerHour: tt.percentPerHour,
					Confidence:     0.8,
					SampleSize:     20,
				},
			}

			pred := e.Predict(context.Background(), usageInfo)

			if pred.Error != "" {
				t.Fatalf("Unexpected error: %s", pred.Error)
			}
			if pred.Warning != tt.wantWarning {
				t.Errorf("Warning = %v, want %v (TimeToDepletion: %v)", pred.Warning, tt.wantWarning, pred.TimeToDepletion)
			}
		})
	}
}

func TestPredictionEngine_Predict_WindowReset(t *testing.T) {
	e := NewPredictionEngine()

	// The current measurement ends in 1 hour. A theoretical 5-hour depletion
	// is not an ETA for this window or a reason to schedule a rotation.
	resetTime := time.Now().Add(1 * time.Hour)
	usageInfo := &usage.UsageInfo{
		Provider:    "claude",
		ProfileName: "test",
		PrimaryWindow: &usage.UsageWindow{
			UsedPercent: 50,
			ResetsAt:    resetTime,
		},
		BurnRate: &usage.BurnRateInfo{
			PercentPerHour: 10, // 50% remaining / 10% per hour = 5 hours
			Confidence:     0.8,
			SampleSize:     15,
		},
	}

	for name, pred := range map[string]*Prediction{
		"api":      e.Predict(context.Background(), usageInfo),
		"external": e.PredictWithBurnRate(usageInfo, usageInfo.BurnRate, "external"),
	} {
		t.Run(name, func(t *testing.T) {
			if !pred.CurrentPercentKnown || pred.CurrentPercent != 50 {
				t.Fatalf("reset discarded the current measurement: %+v", pred)
			}
			if pred.Error == "" || pred.IsValid() || !pred.PredictedTime.IsZero() || pred.TimeToDepletion != 0 || pred.Confidence != 0 || pred.Warning != WarningNone {
				t.Fatalf("reset fabricated a depletion forecast: %+v", pred)
			}
			if pred.ShouldRotate(24*time.Hour) || MostUrgent([]*Prediction{pred}, 0) != nil || len(FilterByWarning([]*Prediction{pred}, WarningApproaching)) != 0 {
				t.Fatal("reset-before-depletion result scheduled a rotation or warning")
			}
			if alerts := GenerateAlerts([]*Prediction{pred}, DefaultAlertOptions()); len(alerts) != 0 {
				t.Fatalf("healthy measurement acquired a reset-based alert: %+v", alerts)
			}
		})
	}
}

func TestPredictionEngine_PredictWithBurnRate(t *testing.T) {
	e := NewPredictionEngine()

	usageInfo := &usage.UsageInfo{
		Provider:    "claude",
		ProfileName: "external",
		PrimaryWindow: &usage.UsageWindow{
			UsedPercent: 70,
			ResetsAt:    time.Now().Add(2 * time.Hour),
		},
	}

	burnRate := &usage.BurnRateInfo{
		PercentPerHour: 30,
		Confidence:     0.9,
		SampleSize:     25,
	}

	pred := e.PredictWithBurnRate(usageInfo, burnRate, "external")

	if pred.Error != "" {
		t.Fatalf("Unexpected error: %s", pred.Error)
	}

	// Should use external source
	if len(pred.DataSources) != 1 || pred.DataSources[0] != "external" {
		t.Errorf("DataSources = %v, want [external]", pred.DataSources)
	}

	// 30% remaining at 30%/hour = 1 hour
	expectedHours := 1.0
	actualHours := pred.TimeToDepletion.Hours()
	if actualHours < expectedHours-0.1 || actualHours > expectedHours+0.1 {
		t.Errorf("TimeToDepletion = %v hours, want ~%v hours", actualHours, expectedHours)
	}
}

func TestPrediction_IsValid(t *testing.T) {
	tests := []struct {
		name string
		pred *Prediction
		want bool
	}{
		{
			name: "valid prediction",
			pred: &Prediction{CurrentPercentKnown: true, DataSources: []string{"session"}},
			want: true,
		},
		{
			name: "nil prediction",
			pred: nil,
			want: false,
		},
		{
			name: "prediction with error",
			pred: &Prediction{CurrentPercentKnown: true, Error: "something went wrong", DataSources: []string{"session"}},
			want: false,
		},
		{
			name: "prediction with no sources",
			pred: &Prediction{CurrentPercentKnown: true},
			want: false,
		},
		{
			name: "unmeasured percentage",
			pred: &Prediction{CurrentPercent: 90, DataSources: []string{"session"}},
			want: false,
		},
		{
			name: "non-finite percentage",
			pred: &Prediction{CurrentPercentKnown: true, CurrentPercent: math.NaN(), DataSources: []string{"session"}},
			want: false,
		},
		{
			name: "non-finite confidence",
			pred: &Prediction{CurrentPercentKnown: true, Confidence: math.Inf(1), DataSources: []string{"session"}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.pred.IsValid(); got != tt.want {
				t.Errorf("IsValid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrediction_ShouldRotate(t *testing.T) {
	tests := []struct {
		name      string
		pred      *Prediction
		threshold time.Duration
		want      bool
	}{
		{
			name: "should rotate - below threshold",
			pred: &Prediction{
				CurrentPercentKnown: true,
				DataSources:         []string{"api"},
				TimeToDepletion:     5 * time.Minute,
				Confidence:          0.8,
			},
			threshold: 10 * time.Minute,
			want:      true,
		},
		{
			name: "should not rotate - above threshold",
			pred: &Prediction{
				CurrentPercentKnown: true,
				DataSources:         []string{"api"},
				TimeToDepletion:     20 * time.Minute,
				Confidence:          0.8,
			},
			threshold: 10 * time.Minute,
			want:      false,
		},
		{
			name: "should not rotate - low confidence",
			pred: &Prediction{
				CurrentPercentKnown: true,
				DataSources:         []string{"api"},
				TimeToDepletion:     5 * time.Minute,
				Confidence:          0.2,
			},
			threshold: 10 * time.Minute,
			want:      false,
		},
		{
			name: "should not rotate - forecast failed",
			pred: &Prediction{
				CurrentPercentKnown: true,
				DataSources:         []string{"api"},
				TimeToDepletion:     5 * time.Minute,
				Confidence:          0.8,
				Error:               "quota resets before predicted depletion",
			},
			threshold: 10 * time.Minute,
			want:      false,
		},
		{
			name:      "nil prediction",
			pred:      nil,
			threshold: 10 * time.Minute,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.pred.ShouldRotate(tt.threshold); got != tt.want {
				t.Errorf("ShouldRotate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWarningLevel_String(t *testing.T) {
	tests := []struct {
		level WarningLevel
		want  string
	}{
		{WarningNone, "none"},
		{WarningApproaching, "approaching"},
		{WarningImminent, "imminent"},
	}

	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("WarningLevel(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestPredictionEngine_PredictAll(t *testing.T) {
	e := NewPredictionEngine()

	profiles := []*usage.UsageInfo{
		{
			Provider:    "claude",
			ProfileName: "profile1",
			PrimaryWindow: &usage.UsageWindow{
				UsedPercent: 30,
			},
			BurnRate: &usage.BurnRateInfo{
				PercentPerHour: 10,
				Confidence:     0.7,
				SampleSize:     10,
			},
		},
		{
			Provider:    "claude",
			ProfileName: "profile2",
			PrimaryWindow: &usage.UsageWindow{
				UsedPercent: 60,
			},
			BurnRate: &usage.BurnRateInfo{
				PercentPerHour: 20,
				Confidence:     0.8,
				SampleSize:     15,
			},
		},
	}

	predictions := e.PredictAll(context.Background(), profiles)

	if len(predictions) != 2 {
		t.Fatalf("PredictAll returned %d predictions, want 2", len(predictions))
	}

	if predictions[0].Profile != "profile1" {
		t.Errorf("predictions[0].Profile = %q, want profile1", predictions[0].Profile)
	}
	if predictions[1].Profile != "profile2" {
		t.Errorf("predictions[1].Profile = %q, want profile2", predictions[1].Profile)
	}
}

func TestMostUrgent(t *testing.T) {
	predictions := []*Prediction{
		{Profile: "slow", CurrentPercentKnown: true, DataSources: []string{"api"}, TimeToDepletion: 2 * time.Hour, Confidence: 0.8},
		{Profile: "fast", CurrentPercentKnown: true, DataSources: []string{"api"}, TimeToDepletion: 30 * time.Minute, Confidence: 0.7},
		{Profile: "fastest", CurrentPercentKnown: true, DataSources: []string{"api"}, TimeToDepletion: 10 * time.Minute, Confidence: 0.9},
		{Profile: "low_confidence", CurrentPercentKnown: true, DataSources: []string{"api"}, TimeToDepletion: 5 * time.Minute, Confidence: 0.2},
		{Profile: "unknown", DataSources: []string{"api"}, TimeToDepletion: time.Minute, Confidence: 1},
		{Profile: "error", Error: "failed"},
	}

	urgent := MostUrgent(predictions, 0.5)

	if urgent == nil {
		t.Fatal("MostUrgent returned nil")
	}
	if urgent.Profile != "fastest" {
		t.Errorf("MostUrgent.Profile = %q, want fastest", urgent.Profile)
	}
}

func TestMostUrgent_Empty(t *testing.T) {
	urgent := MostUrgent(nil, 0.5)
	if urgent != nil {
		t.Errorf("MostUrgent(nil) = %v, want nil", urgent)
	}

	urgent = MostUrgent([]*Prediction{}, 0.5)
	if urgent != nil {
		t.Errorf("MostUrgent([]) = %v, want nil", urgent)
	}
}

func TestFilterByWarning(t *testing.T) {
	predictions := []*Prediction{
		{Profile: "none", CurrentPercentKnown: true, DataSources: []string{"api"}, Warning: WarningNone},
		{Profile: "approaching", CurrentPercentKnown: true, DataSources: []string{"api"}, Warning: WarningApproaching},
		{Profile: "imminent", CurrentPercentKnown: true, DataSources: []string{"api"}, Warning: WarningImminent},
		{Profile: "unknown", Warning: WarningImminent},
		{Profile: "failed", CurrentPercentKnown: true, DataSources: []string{"api"}, Warning: WarningImminent, Error: "forecast failed"},
	}

	// Filter for approaching or higher
	result := FilterByWarning(predictions, WarningApproaching)
	if len(result) != 2 {
		t.Errorf("FilterByWarning(WarningApproaching) returned %d, want 2", len(result))
	}

	// Filter for imminent only
	result = FilterByWarning(predictions, WarningImminent)
	if len(result) != 1 {
		t.Errorf("FilterByWarning(WarningImminent) returned %d, want 1", len(result))
	}
	if result[0].Profile != "imminent" {
		t.Errorf("FilterByWarning result[0].Profile = %q, want imminent", result[0].Profile)
	}
}

func TestPredictionEngine_Predict_InsufficientData(t *testing.T) {
	e := NewPredictionEngine() // No scanner, no tracker

	usageInfo := &usage.UsageInfo{
		Provider:    "claude",
		ProfileName: "test",
		PrimaryWindow: &usage.UsageWindow{
			UsedPercent: 50,
		},
		// No BurnRate set
	}

	pred := e.Predict(context.Background(), usageInfo)

	if pred.Error == "" {
		t.Error("Expected error for insufficient data")
	}
	if len(pred.DataSources) != 0 {
		t.Errorf("DataSources = %v, want empty", pred.DataSources)
	}
	if !pred.CurrentPercentKnown || pred.CurrentPercent != 50 || !pred.PredictedTime.IsZero() || pred.TimeToDepletion != 0 || pred.Confidence != 0 {
		t.Fatalf("missing burn rate discarded a measurement or fabricated a forecast: %+v", pred)
	}
}

func TestPredictionEngineWeeklyOnlyMeasurementWithoutRate(t *testing.T) {
	e := NewPredictionEngine()
	for _, percent := range []int{0, 20, 95} {
		info := &usage.UsageInfo{
			Provider: "claude", ProfileName: "weekly-only",
			SecondaryWindow: &usage.UsageWindow{UsedPercent: percent, WindowDuration: 7 * 24 * time.Hour},
		}
		for name, pred := range map[string]*Prediction{
			"api":      e.Predict(context.Background(), info),
			"external": e.PredictWithBurnRate(info, nil, "external"),
		} {
			if !pred.CurrentPercentKnown || pred.CurrentPercent != float64(percent) {
				t.Errorf("%s lost measured %d%% without a session or rate: %+v", name, percent, pred)
			}
			if pred.Error == "" || pred.IsValid() || pred.TimeToDepletion != 0 || !pred.PredictedTime.IsZero() || pred.Confidence != 0 || pred.Warning != WarningNone || pred.BurnRate != nil {
				t.Errorf("%s fabricated a forecast for measured %d%%: %+v", name, percent, pred)
			}
		}
	}
}

func TestPredictionEngineRejectsUnmeasuredQuota(t *testing.T) {
	measured := &usage.UsageWindow{UsedPercent: 100}
	tests := []struct {
		name string
		info *usage.UsageInfo
	}{
		{"missing", nil},
		{"no windows", &usage.UsageInfo{Provider: "claude"}},
		{"scoped only", &usage.UsageInfo{Provider: "claude", ModelWindows: map[string]*usage.UsageWindow{"fable": measured}}},
		{"error with diagnostic quota", &usage.UsageInfo{Provider: "claude", SecondaryWindow: measured, Error: "unsupported quota kind"}},
		{"degraded", &usage.UsageInfo{Provider: "claude", SecondaryWindow: measured, QuotaStatus: usage.QuotaDegraded}},
		{"limit stage", &usage.UsageInfo{Provider: "claude", SecondaryWindow: measured, LimitStage: "unknown"}},
		{"unmeasured", &usage.UsageInfo{Provider: "claude", SecondaryWindow: &usage.UsageWindow{UsedPercent: 100, Unmeasured: true}}},
		{"rolled cache", &usage.UsageInfo{Provider: "claude", SecondaryWindow: &usage.UsageWindow{Rolled: true}}},
		{"rolled scoped cache", &usage.UsageInfo{Provider: "claude", SecondaryWindow: measured, ScopedWindows: []*usage.UsageWindow{{Label: "Fable", Rolled: true}}}},
		{"NaN", &usage.UsageInfo{Provider: "claude", SecondaryWindow: &usage.UsageWindow{Utilization: math.NaN()}}},
		{"infinite", &usage.UsageInfo{Provider: "claude", SecondaryWindow: &usage.UsageWindow{Utilization: math.Inf(1)}}},
		{"outside range", &usage.UsageInfo{Provider: "claude", SecondaryWindow: &usage.UsageWindow{UsedPercent: 101}}},
	}
	rate := &usage.BurnRateInfo{PercentPerHour: 10, Confidence: 1}
	e := NewPredictionEngine(WithModel("sonnet"))
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for name, pred := range map[string]*Prediction{
				"api":      e.Predict(context.Background(), tc.info),
				"external": e.PredictWithBurnRate(tc.info, rate, "external"),
			} {
				if pred.Error == "" || pred.CurrentPercentKnown || pred.IsValid() || pred.Warning != WarningNone || pred.TimeToDepletion != 0 || !pred.PredictedTime.IsZero() || pred.Confidence != 0 {
					t.Errorf("%s trusted unavailable quota: %+v", name, pred)
				}
			}
		})
	}
}

func TestPredictionEngineModelScopeIncludesGeneralQuota(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		general int
		want    float64
	}{
		{"matching scopes", "sonnet", 20, 40},
		{"general still constrains", "sonnet", 90, 90},
		{"unknown duration still constrains", "fable", 20, 100},
		{"unknown model includes every scope", "", 20, 100},
	}
	rate := &usage.BurnRateInfo{PercentPerHour: 10, Confidence: 0.8, SampleSize: 10}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := &usage.UsageInfo{
				Provider: "claude", ProfileName: "scoped", BurnRate: rate,
				SecondaryWindow: &usage.UsageWindow{UsedPercent: tc.general, WindowDuration: 7 * 24 * time.Hour},
				ModelWindows:    map[string]*usage.UsageWindow{"fable": {Label: "Fable", UsedPercent: 100}},
				ScopedWindows: []*usage.UsageWindow{
					{Label: "Sonnet", UsedPercent: 30, WindowDuration: 7 * 24 * time.Hour},
					{Label: "Sonnet", UsedPercent: 40, WindowDuration: 5 * time.Hour},
				},
			}
			e := NewPredictionEngine(WithModel(tc.model))
			for name, pred := range map[string]*Prediction{
				"api":      e.Predict(context.Background(), info),
				"external": e.PredictWithBurnRate(info, rate, "external"),
			} {
				if !pred.CurrentPercentKnown || pred.CurrentPercent != tc.want || !pred.IsValid() {
					t.Fatalf("%s lost the applicable quota: %+v", name, pred)
				}
				wantDuration := time.Duration((100 - tc.want) / 10 * float64(time.Hour))
				if pred.TimeToDepletion != wantDuration {
					t.Errorf("%s forecast uses an unrelated quota: got %v, want %v", name, pred.TimeToDepletion, wantDuration)
				}
			}
		})
	}
}

func TestPredictionEnginePreservesStricterNumericMeasurement(t *testing.T) {
	e := NewPredictionEngine()
	for _, window := range []*usage.UsageWindow{
		{Utilization: 1, UsedPercent: 20},
		{Utilization: 0.2, UsedPercent: 100},
	} {
		info := &usage.UsageInfo{Provider: "claude", SecondaryWindow: window}
		for name, pred := range map[string]*Prediction{
			"api":      e.Predict(context.Background(), info),
			"external": e.PredictWithBurnRate(info, nil, "external"),
		} {
			if !pred.IsValid() || !pred.CurrentPercentKnown || pred.CurrentPercent != 100 || pred.Warning != WarningImminent || pred.TimeToDepletion != 0 {
				t.Errorf("%s weakened measured exhaustion: %+v", name, pred)
			}
		}
	}
}

func TestPredictionEngineRejectsUnusableRate(t *testing.T) {
	e := NewPredictionEngine()
	for _, rate := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1), math.SmallestNonzeroFloat64, math.MaxFloat64} {
		burnRate := &usage.BurnRateInfo{PercentPerHour: rate, Confidence: 1, SampleSize: 10}
		info := &usage.UsageInfo{Provider: "claude", SecondaryWindow: &usage.UsageWindow{UsedPercent: 20}, BurnRate: burnRate}
		for name, pred := range map[string]*Prediction{
			"api":      e.Predict(context.Background(), info),
			"external": e.PredictWithBurnRate(info, burnRate, "external"),
		} {
			if !pred.CurrentPercentKnown || pred.CurrentPercent != 20 || pred.Error == "" || pred.IsValid() || !pred.PredictedTime.IsZero() || pred.TimeToDepletion != 0 || pred.Confidence != 0 || pred.Warning != WarningNone {
				t.Errorf("%s manufactured forecast for rate %v: %+v", name, rate, pred)
			}
		}
	}
}

func TestPredictionEngine_ConfidenceCalculation(t *testing.T) {
	e := NewPredictionEngine()

	// Test that multiple sources boost confidence
	burnRate := &usage.BurnRateInfo{
		Confidence: 0.7,
		SampleSize: 20,
	}

	conf1 := e.calculateConfidence(burnRate, 1)
	conf2 := e.calculateConfidence(burnRate, 2)

	if conf2 <= conf1 {
		t.Errorf("Multiple sources should boost confidence: 1 source=%v, 2 sources=%v", conf1, conf2)
	}

	// Test small sample penalty
	smallSample := &usage.BurnRateInfo{
		Confidence: 0.8,
		SampleSize: 3,
	}
	largeSample := &usage.BurnRateInfo{
		Confidence: 0.8,
		SampleSize: 20,
	}

	confSmall := e.calculateConfidence(smallSample, 1)
	confLarge := e.calculateConfidence(largeSample, 1)

	if confSmall >= confLarge {
		t.Errorf("Small sample should have lower confidence: small=%v, large=%v", confSmall, confLarge)
	}
}
