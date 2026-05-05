package topotest_test

import (
	"context"
	"testing"
	"time"
)

func newTimeoutCtx(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()

	return context.WithTimeout(t.Context(), d)
}
