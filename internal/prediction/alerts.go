package prediction

import (
	"fmt"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/notify"
)

// AlertType identifies the type of pre-rotation alert.
type AlertType int

const (
	AlertApproachingLimit AlertType = iota
	AlertImminentLimit
	AlertSwitchRecommended
	AlertAllProfilesLow
)

// Urgency indicates alert urgency.
type Urgency int

const (
	UrgencyLow Urgency = iota
	UrgencyMedium
	UrgencyHigh
)

// Alert represents a pre-rotation alert derived from predictions.
type Alert struct {
	Type            AlertType
	Provider        string
	Profile         string
	Message         string
	SuggestedAction string
	TimeUntil       time.Duration
	Urgency         Urgency
	Prediction      *Prediction
}

// AlertOptions controls alert generation thresholds and behavior.
type AlertOptions struct {
	WarningPercent    float64
	CriticalPercent   float64
	RotationThreshold time.Duration
	MinConfidence     float64
}

// DefaultAlertOptions returns default thresholds aligned with config defaults.
func DefaultAlertOptions() AlertOptions {
	return AlertOptions{
		WarningPercent:    70,
		CriticalPercent:   85,
		RotationThreshold: 30 * time.Minute,
		MinConfidence:     0.3,
	}
}

// GenerateAlerts creates pre-rotation alerts for the given predictions.
func GenerateAlerts(predictions []*Prediction, opts AlertOptions) []*Alert {
	if opts == (AlertOptions{}) {
		opts = DefaultAlertOptions()
	}
	if opts.WarningPercent < 0 {
		opts.WarningPercent = DefaultAlertOptions().WarningPercent
	}
	if opts.CriticalPercent < 0 {
		opts.CriticalPercent = DefaultAlertOptions().CriticalPercent
	}
	if opts.RotationThreshold < 0 {
		opts.RotationThreshold = DefaultAlertOptions().RotationThreshold
	}
	if opts.MinConfidence < 0 {
		opts.MinConfidence = DefaultAlertOptions().MinConfidence
	}

	var alerts []*Alert
	allLow := len(predictions) > 0

	for _, pred := range predictions {
		if !pred.hasCurrentPercent() {
			// Unknown accounts cannot establish that all monitored accounts
			// are low, nor can their zero values stand in for healthy usage.
			allLow = false
			continue
		}

		var timeUntil time.Duration
		if pred.IsValid() {
			timeUntil = pred.TimeToDepletion
		}

		alertType, urgency := classifyAlert(pred, opts)
		if alertType == nil {
			// A known zero is just as much healthy evidence as a known 20%.
			allLow = false
		} else {
			alerts = append(alerts, &Alert{
				Type:            *alertType,
				Provider:        pred.Provider,
				Profile:         pred.Profile,
				Message:         buildMessage(pred),
				SuggestedAction: buildAction(pred),
				TimeUntil:       timeUntil,
				Urgency:         urgency,
				Prediction:      pred,
			})
		}

		if shouldRecommendSwitch(pred, opts) {
			alerts = append(alerts, &Alert{
				Type:            AlertSwitchRecommended,
				Provider:        pred.Provider,
				Profile:         pred.Profile,
				Message:         "Rotation recommended based on burn rate",
				SuggestedAction: buildAction(pred),
				TimeUntil:       timeUntil,
				Urgency:         UrgencyHigh,
				Prediction:      pred,
			})
		}
	}

	if allLow {
		alerts = append(alerts, &Alert{
			Type:            AlertAllProfilesLow,
			Message:         "All monitored profiles are near rate limits",
			SuggestedAction: "Consider waiting for reset or adding accounts",
			Urgency:         UrgencyHigh,
		})
	}

	return alerts
}

// ToNotifyAlert converts a prediction alert to a notify.Alert.
func ToNotifyAlert(a *Alert) *notify.Alert {
	if a == nil {
		return nil
	}

	level := notify.Info
	switch a.Urgency {
	case UrgencyHigh:
		level = notify.Critical
	case UrgencyMedium:
		level = notify.Warning
	}

	title := "Pre-rotation alert"
	switch a.Type {
	case AlertImminentLimit:
		title = "Rate limit imminent"
	case AlertApproachingLimit:
		title = "Rate limit approaching"
	case AlertSwitchRecommended:
		title = "Rotation recommended"
	case AlertAllProfilesLow:
		title = "All profiles low"
	}

	return &notify.Alert{
		Level:     level,
		Title:     title,
		Message:   a.Message,
		Profile:   a.Profile,
		Action:    a.SuggestedAction,
		Timestamp: time.Now(),
	}
}

func classifyAlert(pred *Prediction, opts AlertOptions) (*AlertType, Urgency) {
	if !pred.hasCurrentPercent() {
		return nil, UrgencyLow
	}

	percent := pred.CurrentPercent
	forecastValid := pred.IsValid()
	imminent := (forecastValid && pred.Warning >= WarningImminent) || (opts.CriticalPercent > 0 && percent >= opts.CriticalPercent)
	approaching := (forecastValid && pred.Warning >= WarningApproaching) || (opts.WarningPercent > 0 && percent >= opts.WarningPercent)

	switch {
	case imminent:
		alertType := AlertImminentLimit
		return &alertType, UrgencyHigh
	case approaching:
		alertType := AlertApproachingLimit
		return &alertType, UrgencyMedium
	default:
		return nil, UrgencyLow
	}
}

func shouldRecommendSwitch(pred *Prediction, opts AlertOptions) bool {
	if !pred.IsValid() || pred.TimeToDepletion <= 0 {
		return false
	}
	if pred.Confidence < opts.MinConfidence {
		return false
	}
	return pred.TimeToDepletion < opts.RotationThreshold
}

func buildMessage(pred *Prediction) string {
	if !pred.hasCurrentPercent() {
		return "Rate limit status unknown"
	}

	if pred.IsValid() && pred.TimeToDepletion > 0 {
		return fmt.Sprintf("Will hit limit in ~%s (%.0f%% used)", pred.TimeToDepletion.Round(time.Minute), pred.CurrentPercent)
	}
	return fmt.Sprintf("Usage at %.0f%%", pred.CurrentPercent)
}

func buildAction(pred *Prediction) string {
	if pred == nil || pred.Provider == "" {
		return "Consider switching to another profile"
	}
	return fmt.Sprintf("caam activate %s --auto", pred.Provider)
}
