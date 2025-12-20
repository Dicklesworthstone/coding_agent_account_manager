// Package testutil provides E2E test infrastructure with detailed logging.
//
// ExtendedHarness wraps TestHarness to provide step tracking, metrics
// collection, and detailed logging for E2E integration tests.
package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// ExtendedHarness - Enhanced test harness with step tracking and metrics
// =============================================================================

// StepLog represents a single step in a test with timing information.
type StepLog struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	StartTime   time.Time              `json:"start_time"`
	EndTime     time.Time              `json:"end_time,omitempty"`
	Duration    time.Duration          `json:"duration,omitempty"`
	Level       LogLevel               `json:"level"`
	Message     string                 `json:"message,omitempty"`
	Data        map[string]interface{} `json:"data,omitempty"`
	Nested      []*StepLog             `json:"nested,omitempty"`
	completed   bool
}

// ExtendedHarness extends TestHarness with step tracking and metrics.
type ExtendedHarness struct {
	*TestHarness

	mu         sync.Mutex
	logBuffer  *bytes.Buffer
	stepLogs   []*StepLog
	stepStack  []*StepLog // For nested steps
	metrics    map[string]time.Duration
	startTime  time.Time
	stepCount  int
	errorCount int
}

// NewExtendedHarness creates a new extended harness wrapping TestHarness.
func NewExtendedHarness(t *testing.T) *ExtendedHarness {
	h := NewHarness(t)
	return &ExtendedHarness{
		TestHarness: h,
		logBuffer:   &bytes.Buffer{},
		stepLogs:    make([]*StepLog, 0),
		stepStack:   make([]*StepLog, 0),
		metrics:     make(map[string]time.Duration),
		startTime:   time.Now(),
	}
}

// =============================================================================
// Step Tracking Methods
// =============================================================================

// StartStep begins a new named step with optional description.
// Steps can be nested - starting a step while another is active
// makes the new step a child of the active step.
func (h *ExtendedHarness) StartStep(name, description string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	step := &StepLog{
		Name:        name,
		Description: description,
		StartTime:   time.Now(),
		Level:       INFO,
		Nested:      make([]*StepLog, 0),
	}

	// If there's a parent step on the stack, add as nested
	if len(h.stepStack) > 0 {
		parent := h.stepStack[len(h.stepStack)-1]
		parent.Nested = append(parent.Nested, step)
	} else {
		h.stepLogs = append(h.stepLogs, step)
	}

	h.stepStack = append(h.stepStack, step)
	h.stepCount++

	// Update the underlying logger's step context
	h.Log.SetStep(name)

	// Log the step start
	h.writeLog(INFO, fmt.Sprintf("START: %s", name), map[string]interface{}{
		"description": description,
		"nested":      len(h.stepStack) > 1,
	})
}

// EndStep completes the current step, recording its duration.
// If no step is active, this is a no-op.
func (h *ExtendedHarness) EndStep(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Find and complete the step
	if len(h.stepStack) == 0 {
		h.Log.Warn(fmt.Sprintf("EndStep called for %q but no step is active", name))
		return
	}

	// Pop from stack
	step := h.stepStack[len(h.stepStack)-1]
	h.stepStack = h.stepStack[:len(h.stepStack)-1]

	if step.Name != name {
		h.Log.Warn(fmt.Sprintf("EndStep called for %q but active step is %q", name, step.Name))
	}

	step.EndTime = time.Now()
	step.Duration = step.EndTime.Sub(step.StartTime)
	step.completed = true

	// Record as a metric
	h.metrics[fmt.Sprintf("step.%s", name)] = step.Duration

	// Update logger context
	if len(h.stepStack) > 0 {
		h.Log.SetStep(h.stepStack[len(h.stepStack)-1].Name)
	} else {
		h.Log.SetStep("")
	}

	// Log step completion
	h.writeLog(INFO, fmt.Sprintf("END: %s", name), map[string]interface{}{
		"duration_ms": step.Duration.Milliseconds(),
	})
}

// CurrentStep returns the name of the current active step, or empty string.
func (h *ExtendedHarness) CurrentStep() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.currentStepUnsafe()
}

// currentStepUnsafe returns the current step name without locking.
// Caller must hold h.mu.
func (h *ExtendedHarness) currentStepUnsafe() string {
	if len(h.stepStack) == 0 {
		return ""
	}
	return h.stepStack[len(h.stepStack)-1].Name
}

// =============================================================================
// Logging Methods
// =============================================================================

// writeLog writes a log entry to the buffer and underlying logger.
// Caller must hold h.mu.
func (h *ExtendedHarness) writeLog(level LogLevel, msg string, data map[string]interface{}) {
	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     level.String(),
		Test:      h.T.Name(),
		Step:      h.currentStepUnsafe(),
		Message:   msg,
		Duration:  time.Since(h.startTime).String(),
		Data:      data,
	}

	// Write to buffer as JSON
	jsonBytes, _ := json.Marshal(entry)
	h.logBuffer.Write(jsonBytes)
	h.logBuffer.WriteString("\n")

	// Also log via underlying logger
	switch level {
	case DEBUG:
		h.Log.Debug(msg, data)
	case INFO:
		h.Log.Info(msg, data)
	case WARN:
		h.Log.Warn(msg, data)
	case ERROR:
		h.Log.Error(msg, data)
	}
}

// LogInfo logs an informational message.
func (h *ExtendedHarness) LogInfo(msg string, data ...interface{}) {
	h.mu.Lock()
	defer h.mu.Unlock()

	d := h.parseData(data...)
	h.writeLog(INFO, msg, d)
}

// LogDebug logs a debug message.
func (h *ExtendedHarness) LogDebug(msg string, data ...interface{}) {
	h.mu.Lock()
	defer h.mu.Unlock()

	d := h.parseData(data...)
	h.writeLog(DEBUG, msg, d)
}

// LogWarn logs a warning message.
func (h *ExtendedHarness) LogWarn(msg string, data ...interface{}) {
	h.mu.Lock()
	defer h.mu.Unlock()

	d := h.parseData(data...)
	h.writeLog(WARN, msg, d)
}

// LogError logs an error with context.
func (h *ExtendedHarness) LogError(err error, context string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.errorCount++
	d := map[string]interface{}{
		"error":   err.Error(),
		"context": context,
	}
	h.writeLog(ERROR, fmt.Sprintf("ERROR: %s", context), d)
}

// parseData converts variadic args to a data map.
// Accepts: map[string]interface{}, key-value pairs, or nothing.
func (h *ExtendedHarness) parseData(data ...interface{}) map[string]interface{} {
	if len(data) == 0 {
		return nil
	}

	// If first arg is already a map, use it
	if m, ok := data[0].(map[string]interface{}); ok {
		return m
	}

	// Otherwise treat as key-value pairs
	result := make(map[string]interface{})
	for i := 0; i < len(data)-1; i += 2 {
		if key, ok := data[i].(string); ok {
			result[key] = data[i+1]
		}
	}
	return result
}

// =============================================================================
// Metrics Methods
// =============================================================================

// RecordMetric records a named duration metric.
func (h *ExtendedHarness) RecordMetric(name string, value time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.metrics[name] = value
	h.writeLog(DEBUG, fmt.Sprintf("Recorded metric: %s = %v", name, value), map[string]interface{}{
		"metric": name,
		"value":  value.String(),
	})
}

// GetMetric returns a recorded metric, or 0 if not found.
func (h *ExtendedHarness) GetMetric(name string) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.metrics[name]
}

// Metrics returns a copy of all recorded metrics.
func (h *ExtendedHarness) Metrics() map[string]time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()

	result := make(map[string]time.Duration, len(h.metrics))
	for k, v := range h.metrics {
		result[k] = v
	}
	return result
}

// =============================================================================
// Summary and Output Methods
// =============================================================================

// DumpLogs returns all logged entries as a formatted string.
func (h *ExtendedHarness) DumpLogs() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.logBuffer.String()
}

// DumpLogsJSON returns all logged entries as a JSON array string.
func (h *ExtendedHarness) DumpLogsJSON() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Parse the newline-delimited JSON
	lines := strings.Split(strings.TrimSpace(h.logBuffer.String()), "\n")
	entries := make([]json.RawMessage, 0, len(lines))
	for _, line := range lines {
		if line != "" {
			entries = append(entries, json.RawMessage(line))
		}
	}

	result, _ := json.MarshalIndent(entries, "", "  ")
	return string(result)
}

// Summary returns a human-readable summary of the test execution.
func (h *ExtendedHarness) Summary() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	var sb strings.Builder
	totalDuration := time.Since(h.startTime)

	sb.WriteString("═══════════════════════════════════════════════════════════\n")
	sb.WriteString(fmt.Sprintf("  TEST SUMMARY: %s\n", h.T.Name()))
	sb.WriteString("═══════════════════════════════════════════════════════════\n\n")

	// Overall stats
	sb.WriteString("📊 OVERALL STATISTICS\n")
	sb.WriteString("───────────────────────────────────────────────────────────\n")
	sb.WriteString(fmt.Sprintf("  Total Duration: %v\n", totalDuration.Round(time.Millisecond)))
	sb.WriteString(fmt.Sprintf("  Steps Executed: %d\n", h.stepCount))
	sb.WriteString(fmt.Sprintf("  Errors: %d\n", h.errorCount))
	sb.WriteString("\n")

	// Step timeline
	if len(h.stepLogs) > 0 {
		sb.WriteString("📋 STEP TIMELINE\n")
		sb.WriteString("───────────────────────────────────────────────────────────\n")
		h.formatStepTimeline(&sb, h.stepLogs, 0)
		sb.WriteString("\n")
	}

	// Metrics
	if len(h.metrics) > 0 {
		sb.WriteString("⏱️  METRICS\n")
		sb.WriteString("───────────────────────────────────────────────────────────\n")

		// Sort metrics by name
		names := make([]string, 0, len(h.metrics))
		for name := range h.metrics {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			duration := h.metrics[name]
			sb.WriteString(fmt.Sprintf("  %-40s %v\n", name, duration.Round(time.Microsecond)))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("═══════════════════════════════════════════════════════════\n")

	return sb.String()
}

// formatStepTimeline recursively formats steps with indentation.
func (h *ExtendedHarness) formatStepTimeline(sb *strings.Builder, steps []*StepLog, indent int) {
	prefix := strings.Repeat("  ", indent)
	for _, step := range steps {
		status := "✓"
		if !step.completed {
			status = "○"
		}
		durationStr := "-"
		if step.Duration > 0 {
			durationStr = step.Duration.Round(time.Microsecond).String()
		}
		sb.WriteString(fmt.Sprintf("%s  %s %-35s %s\n", prefix, status, step.Name, durationStr))
		if step.Description != "" {
			sb.WriteString(fmt.Sprintf("%s      %s\n", prefix, step.Description))
		}
		if len(step.Nested) > 0 {
			h.formatStepTimeline(sb, step.Nested, indent+1)
		}
	}
}

// SummaryJSON returns a JSON-formatted summary of the test execution.
func (h *ExtendedHarness) SummaryJSON() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	summary := struct {
		TestName      string                 `json:"test_name"`
		TotalDuration string                 `json:"total_duration"`
		DurationMs    int64                  `json:"duration_ms"`
		StepCount     int                    `json:"step_count"`
		ErrorCount    int                    `json:"error_count"`
		Steps         []*StepLog             `json:"steps"`
		Metrics       map[string]interface{} `json:"metrics"`
	}{
		TestName:      h.T.Name(),
		TotalDuration: time.Since(h.startTime).String(),
		DurationMs:    time.Since(h.startTime).Milliseconds(),
		StepCount:     h.stepCount,
		ErrorCount:    h.errorCount,
		Steps:         h.stepLogs,
		Metrics:       make(map[string]interface{}),
	}

	// Convert metrics to string values for JSON
	for k, v := range h.metrics {
		summary.Metrics[k] = v.String()
	}

	result, _ := json.MarshalIndent(summary, "", "  ")
	return string(result)
}

// =============================================================================
// Extended Close with Summary
// =============================================================================

// Close cleans up the harness and logs a summary if verbose.
func (h *ExtendedHarness) Close() {
	// Complete any open steps
	h.mu.Lock()
	openSteps := len(h.stepStack)
	h.mu.Unlock()

	for openSteps > 0 {
		h.EndStep(h.CurrentStep())
		h.mu.Lock()
		openSteps = len(h.stepStack)
		h.mu.Unlock()
	}

	// Log summary if verbose
	if testing.Verbose() {
		h.T.Log("\n" + h.Summary())
	}

	// Call parent close
	h.TestHarness.Close()
}

// =============================================================================
// Timing Helpers
// =============================================================================

// TimeIt executes a function and returns its duration.
// The duration is also recorded as a metric with the given name.
func (h *ExtendedHarness) TimeIt(name string, fn func()) time.Duration {
	start := time.Now()
	fn()
	duration := time.Since(start)
	h.RecordMetric(name, duration)
	return duration
}

// TimeStep executes a function within a named step, recording duration.
func (h *ExtendedHarness) TimeStep(name, description string, fn func()) time.Duration {
	h.StartStep(name, description)
	defer h.EndStep(name)
	return h.TimeIt(fmt.Sprintf("step.%s.inner", name), fn)
}
