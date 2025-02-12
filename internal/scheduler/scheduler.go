package scheduler

import (
	"context"
	"time"
)


func After(cancel context.Context, after time.Duration, fn func()) {
	go func() {
		select {
		case <-cancel.Done():
		case <-time.After(after):
			fn()
		}
	}()
}
