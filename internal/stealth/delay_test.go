package stealth

import (
	"context"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestComputeDelay(t *testing.T) {
	t.Run("valid range", func(t *testing.T) {
		rng := rand.New(rand.NewSource(1))
		got, err := ComputeDelay(5, 30, rng)
		if err != nil {
			t.Fatalf("ComputeDelay() error = %v", err)
		}
		if got < 5*time.Second || got > 30*time.Second {
			t.Fatalf("ComputeDelay() = %v, want within [5s, 30s]", got)
		}
	})

	t.Run("equal bounds", func(t *testing.T) {
		got, err := ComputeDelay(7, 7, rand.New(rand.NewSource(1)))
		if err != nil {
			t.Fatalf("ComputeDelay() error = %v", err)
		}
		if got != 7*time.Second {
			t.Fatalf("ComputeDelay() = %v, want %v", got, 7*time.Second)
		}
	})

	t.Run("invalid bounds", func(t *testing.T) {
		if _, err := ComputeDelay(10, 5, rand.New(rand.NewSource(1))); err == nil {
			t.Fatal("ComputeDelay() should error when min > max")
		}
	})
}

func TestComputeDelay_NegativeMinSeconds(t *testing.T) {
	_, err := ComputeDelay(-1, 10, nil)
	if err == nil {
		t.Fatal("ComputeDelay() should error when minSeconds is negative")
	}
	if err.Error() != "minSeconds cannot be negative" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestComputeDelay_NegativeMaxSeconds(t *testing.T) {
	_, err := ComputeDelay(0, -1, nil)
	if err == nil {
		t.Fatal("ComputeDelay() should error when maxSeconds is negative")
	}
	if err.Error() != "maxSeconds cannot be negative" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestComputeDelay_NilRng(t *testing.T) {
	// When rng is nil, it should create a new one internally
	got, err := ComputeDelay(5, 10, nil)
	if err != nil {
		t.Fatalf("ComputeDelay() error = %v", err)
	}
	if got < 5*time.Second || got > 10*time.Second {
		t.Fatalf("ComputeDelay() = %v, want within [5s, 10s]", got)
	}
}

func TestComputeDelay_ZeroRange(t *testing.T) {
	got, err := ComputeDelay(0, 0, nil)
	if err != nil {
		t.Fatalf("ComputeDelay() error = %v", err)
	}
	if got != 0 {
		t.Fatalf("ComputeDelay() = %v, want 0", got)
	}
}

func TestWait(t *testing.T) {
	t.Run("skips when Skip channel fires", func(t *testing.T) {
		skip := make(chan struct{})
		close(skip)

		skipped, err := Wait(context.Background(), 10*time.Second, WaitOptions{
			Skip: skip,
		})
		if err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
		if !skipped {
			t.Fatal("Wait() skipped = false, want true")
		}
	})

	t.Run("returns ctx error when canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		skipped, err := Wait(ctx, 10*time.Second, WaitOptions{})
		if err == nil {
			t.Fatal("Wait() error = nil, want context cancellation")
		}
		if skipped {
			t.Fatal("Wait() skipped = true, want false")
		}
	})

	t.Run("no-op on zero duration", func(t *testing.T) {
		skipped, err := Wait(context.Background(), 0, WaitOptions{})
		if err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
		if skipped {
			t.Fatal("Wait() skipped = true, want false")
		}
	})
}

func TestWait_NilContext(t *testing.T) {
	// When ctx is nil, it should use context.Background()
	skip := make(chan struct{})
	close(skip)

	skipped, err := Wait(nil, 10*time.Second, WaitOptions{
		Skip: skip,
	})
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !skipped {
		t.Fatal("Wait() skipped = false, want true")
	}
}

func TestWait_NegativeDuration(t *testing.T) {
	skipped, err := Wait(context.Background(), -1*time.Second, WaitOptions{})
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if skipped {
		t.Fatal("Wait() skipped = true, want false")
	}
}

func TestWait_CompletesSuccessfully(t *testing.T) {
	start := time.Now()
	skipped, err := Wait(context.Background(), 50*time.Millisecond, WaitOptions{})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if skipped {
		t.Fatal("Wait() skipped = true, want false")
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("Wait() returned too quickly: %v", elapsed)
	}
}

func TestWait_WithCountdown(t *testing.T) {
	var buf strings.Builder

	skipped, err := Wait(context.Background(), 100*time.Millisecond, WaitOptions{
		Output:        &buf,
		ShowCountdown: true,
		BarWidth:      10,
		Tick:          20 * time.Millisecond,
	})

	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if skipped {
		t.Fatal("Wait() skipped = true, want false")
	}

	output := buf.String()
	// Should contain progress bar characters
	if !strings.Contains(output, "█") && !strings.Contains(output, "░") {
		t.Errorf("output should contain progress bar characters, got: %q", output)
	}
	// Should contain "remaining" text
	if !strings.Contains(output, "remaining") {
		t.Errorf("output should contain 'remaining', got: %q", output)
	}
}

func TestWait_WithCountdownSkipped(t *testing.T) {
	var buf strings.Builder
	skip := make(chan struct{})

	// Close skip after a short delay
	go func() {
		time.Sleep(30 * time.Millisecond)
		close(skip)
	}()

	skipped, err := Wait(context.Background(), 1*time.Second, WaitOptions{
		Output:        &buf,
		ShowCountdown: true,
		BarWidth:      10,
		Tick:          10 * time.Millisecond,
		Skip:          skip,
	})

	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !skipped {
		t.Fatal("Wait() skipped = false, want true")
	}
}

func TestWait_WithCountdownCanceled(t *testing.T) {
	var buf strings.Builder
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	skipped, err := Wait(ctx, 1*time.Second, WaitOptions{
		Output:        &buf,
		ShowCountdown: true,
		BarWidth:      10,
		Tick:          10 * time.Millisecond,
	})

	if err == nil {
		t.Fatal("Wait() error = nil, want context cancellation")
	}
	if skipped {
		t.Fatal("Wait() skipped = true, want false")
	}
}

func TestWait_DefaultOptions(t *testing.T) {
	// Test that defaults are applied correctly
	skip := make(chan struct{})
	close(skip)

	// nil Output should be replaced with io.Discard
	// zero BarWidth should default to 30
	// zero Tick should default to time.Second
	skipped, err := Wait(context.Background(), 10*time.Second, WaitOptions{
		Skip: skip,
		// Output: nil (default)
		// BarWidth: 0 (default)
		// Tick: 0 (default)
	})

	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !skipped {
		t.Fatal("Wait() skipped = false, want true")
	}
}
