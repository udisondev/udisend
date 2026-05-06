package httpui_test

import (
	"testing"
	"time"
)

// TestNextTOTPStepTime — the helper's whole purpose is the boundary
// math (Truncate(period) + period + 1s lands inside step+1 regardless
// of phase). A regression here silently restores the original
// flake (TOTP step boundary causes ~17% test failure rate).
//
// Property: for any input now, nextTOTPStepTime(now) ∈ [step+1 start,
// step+1 end), where step = floor(now.Unix() / 30) and the step
// window is [step*30, (step+1)*30).
func TestNextTOTPStepTime(t *testing.T) {
	t.Parallel()

	const period = 30 * time.Second
	// Use a fixed Unix epoch as the base so subsecond / timezone
	// quirks of the test host don't perturb the math.
	base := time.Unix(0, 0).UTC()

	cases := []struct {
		name      string
		offsetSec int64 // offset from base, in seconds
	}{
		{"start of step", 0},
		{"1s into step", 1},
		{"mid-step", 15},
		{"1s before next step", 29},
		{"start of next step", 30},
		{"mid second step", 45},
		{"end of second step", 59},
		{"large offset, mid-step", 12345},
		{"large offset, step boundary", 12330},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := base.Add(time.Duration(tc.offsetSec) * time.Second)
			got := nextTOTPStepTime(now)

			currentStep := now.Unix() / 30
			gotStep := got.Unix() / 30
			if gotStep != currentStep+1 {
				t.Fatalf("nextTOTPStepTime(%s) gave step %d, want step+1 = %d (current=%d)",
					now, gotStep, currentStep+1, currentStep)
			}

			// Stay safely inside step+1 — within [start+0, start+period).
			stepStart := time.Unix((currentStep+1)*30, 0).UTC()
			stepEnd := stepStart.Add(period)
			if got.Before(stepStart) || !got.Before(stepEnd) {
				t.Fatalf("nextTOTPStepTime(%s) = %s; want in [%s, %s)",
					now, got, stepStart, stepEnd)
			}

			// And the leading 1s buffer keeps us off the boundary,
			// so a near-boundary scheduler hiccup can't push the
			// TOTP code into step+2 at validation time.
			if got.Sub(stepStart) < time.Second {
				t.Fatalf("nextTOTPStepTime(%s) = %s; only %s past step start, want ≥ 1s buffer",
					now, got, got.Sub(stepStart))
			}
		})
	}
}
