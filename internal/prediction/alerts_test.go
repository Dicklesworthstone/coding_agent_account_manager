package prediction

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

func TestGenerateAlerts_ApproachingAndImminent(t *testing.T) {
	approaching := &Prediction{
		Provider:            "claude",
		Profile:             "work",
		CurrentPercent:      50,
		CurrentPercentKnown: true,
		DataSources:         []string{"api"},
		TimeToDepletion:     25 * time.Minute,
		Warning:             WarningApproaching,
		Confidence:          0.8,
	}
	imminent := &Prediction{
		Provider:            "codex",
		Profile:             "personal",
		CurrentPercent:      90,
		CurrentPercentKnown: true,
		DataSources:         []string{"api"},
		TimeToDepletion:     5 * time.Minute,
		Warning:             WarningNone,
		Confidence:          0.9,
	}

	alerts := GenerateAlerts([]*Prediction{approaching, imminent}, DefaultAlertOptions())

	if findAlert(alerts, AlertApproachingLimit, "work") == nil {
		t.Fatalf("expected approaching alert for work profile")
	}
	if findAlert(alerts, AlertImminentLimit, "personal") == nil {
		t.Fatalf("expected imminent alert for personal profile")
	}
}

func TestGenerateAlerts_SwitchRecommended(t *testing.T) {
	pred := &Prediction{
		Provider:            "claude",
		Profile:             "work",
		CurrentPercentKnown: true,
		DataSources:         []string{"api"},
		TimeToDepletion:     10 * time.Minute,
		Warning:             WarningApproaching,
		Confidence:          0.8,
	}

	alerts := GenerateAlerts([]*Prediction{pred}, DefaultAlertOptions())
	if findAlert(alerts, AlertSwitchRecommended, "work") == nil {
		t.Fatalf("expected switch recommended alert")
	}
}

func TestGenerateAlerts_AllProfilesLow(t *testing.T) {
	opts := DefaultAlertOptions()
	p1 := &Prediction{Profile: "one", CurrentPercentKnown: true, CurrentPercent: opts.WarningPercent + 1, Warning: WarningNone}
	p2 := &Prediction{Profile: "two", CurrentPercentKnown: true, CurrentPercent: opts.WarningPercent + 5, Warning: WarningApproaching}

	alerts := GenerateAlerts([]*Prediction{p1, p2}, opts)
	if findAlert(alerts, AlertAllProfilesLow, "") == nil {
		t.Fatalf("expected all profiles low alert")
	}
}

func TestToNotifyAlert(t *testing.T) {
	alert := &Alert{
		Type:      AlertImminentLimit,
		Profile:   "work",
		Message:   "Will hit limit soon",
		Urgency:   UrgencyHigh,
		TimeUntil: 5 * time.Minute,
	}

	notifyAlert := ToNotifyAlert(alert)
	if notifyAlert == nil {
		t.Fatal("expected notify alert")
	}
	if notifyAlert.Profile != "work" {
		t.Fatalf("notify profile = %q, want work", notifyAlert.Profile)
	}
	if notifyAlert.Title != "Rate limit imminent" {
		t.Fatalf("notify title = %q, want rate limit imminent", notifyAlert.Title)
	}
}

func TestGenerateAlertsRetainsHealthyMeasurementsWithoutRates(t *testing.T) {
	e := NewPredictionEngine()
	for _, healthyPercent := range []int{0, 20} {
		predictions := e.PredictAll(context.Background(), []*usage.UsageInfo{
			{Provider: "claude", ProfileName: "healthy", SecondaryWindow: &usage.UsageWindow{UsedPercent: healthyPercent}},
			{Provider: "claude", ProfileName: "spent", SecondaryWindow: &usage.UsageWindow{UsedPercent: 100}},
		})
		alerts := GenerateAlerts(predictions, DefaultAlertOptions())
		if len(alerts) != 1 || findAlert(alerts, AlertImminentLimit, "spent") == nil {
			t.Errorf("measured %d%% did not prevent false aggregate/timed alerts: %+v", healthyPercent, alerts)
		}
	}
}

func TestGenerateAlertsAllMeasuredProfilesLowWithoutRates(t *testing.T) {
	e := NewPredictionEngine()
	predictions := e.PredictAll(context.Background(), []*usage.UsageInfo{
		{Provider: "claude", ProfileName: "one", SecondaryWindow: &usage.UsageWindow{UsedPercent: 85}},
		{Provider: "claude", ProfileName: "two", SecondaryWindow: &usage.UsageWindow{UsedPercent: 95}},
	})
	alerts := GenerateAlerts(predictions, DefaultAlertOptions())
	if findAlert(alerts, AlertAllProfilesLow, "") == nil || findAlert(alerts, AlertImminentLimit, "one") == nil || findAlert(alerts, AlertImminentLimit, "two") == nil {
		t.Fatalf("missing burn rates suppressed measured quota warnings: %+v", alerts)
	}
	for _, alert := range alerts {
		if alert.TimeUntil != 0 || strings.Contains(alert.Message, "Will hit limit") || alert.Type == AlertSwitchRecommended {
			t.Errorf("measurement-only result fabricated timing: %+v", alert)
		}
	}
}

func TestGenerateAlertsUnknownInputsPreventAggregateClaims(t *testing.T) {
	spent := &Prediction{Profile: "spent", CurrentPercentKnown: true, CurrentPercent: 100}
	for _, unknown := range []*Prediction{
		nil,
		{},
		{Profile: "failed", Error: "no measured quota available"},
		{Profile: "unmeasured", CurrentPercent: 100, Warning: WarningImminent, TimeToDepletion: time.Minute, DataSources: []string{"api"}, Confidence: 1},
		{Profile: "nan", CurrentPercentKnown: true, CurrentPercent: math.NaN()},
		{Profile: "infinite", CurrentPercentKnown: true, CurrentPercent: math.Inf(1)},
		{Profile: "negative", CurrentPercentKnown: true, CurrentPercent: -1},
		{Profile: "out of range", CurrentPercentKnown: true, CurrentPercent: 101},
	} {
		alerts := GenerateAlerts([]*Prediction{spent, unknown}, DefaultAlertOptions())
		if len(alerts) != 1 || findAlert(alerts, AlertImminentLimit, "spent") == nil {
			t.Errorf("unknown input fabricated or suppressed quota alerts: input=%+v alerts=%+v", unknown, alerts)
		}
	}
	if alerts := GenerateAlerts(nil, DefaultAlertOptions()); len(alerts) != 0 {
		t.Errorf("empty input fabricated all-profiles-low: %+v", alerts)
	}
}

func TestGenerateAlertsRejectsFailedForecastTiming(t *testing.T) {
	for _, percent := range []float64{20, 95} {
		pred := &Prediction{
			Provider: "claude", Profile: "known", CurrentPercentKnown: true, CurrentPercent: percent,
			Error: "forecast unavailable", DataSources: []string{"api"},
			TimeToDepletion: time.Minute, Warning: WarningImminent, Confidence: 1,
		}
		alerts := GenerateAlerts([]*Prediction{pred}, DefaultAlertOptions())
		if percent == 20 && len(alerts) != 0 {
			t.Errorf("failed forecast manufactured a warning for healthy usage: %+v", alerts)
		}
		if percent == 95 && findAlert(alerts, AlertImminentLimit, "known") == nil {
			t.Errorf("failed forecast discarded the high measured percentage: %+v", alerts)
		}
		for _, alert := range alerts {
			if alert.TimeUntil != 0 || strings.Contains(alert.Message, "Will hit limit") || alert.Type == AlertSwitchRecommended {
				t.Errorf("failed forecast manufactured timing: %+v", alert)
			}
		}
	}
}

func TestGenerateAlertsResetBeforeDepletionKeepsOnlyMeasuredWarning(t *testing.T) {
	info := &usage.UsageInfo{
		Provider: "claude", ProfileName: "resetting",
		SecondaryWindow: &usage.UsageWindow{UsedPercent: 90, ResetsAt: time.Now().Add(time.Minute)},
		BurnRate:        &usage.BurnRateInfo{PercentPerHour: 60, Confidence: 1, SampleSize: 10},
	}
	pred := NewPredictionEngine().Predict(context.Background(), info)
	alerts := GenerateAlerts([]*Prediction{pred}, DefaultAlertOptions())
	alert := findAlert(alerts, AlertImminentLimit, "resetting")
	if alert == nil || alert.Message != "Usage at 90%" || alert.TimeUntil != 0 {
		t.Fatalf("reset manufactured an ETA or hid measured usage: %+v", alert)
	}
	if findAlert(alerts, AlertSwitchRecommended, "") != nil {
		t.Fatal("reset-before-depletion scheduled a timed rotation")
	}
}

func findAlert(alerts []*Alert, alertType AlertType, profile string) *Alert {
	for _, alert := range alerts {
		if alert == nil {
			continue
		}
		if alert.Type != alertType {
			continue
		}
		if profile != "" && alert.Profile != profile {
			continue
		}
		return alert
	}
	return nil
}
