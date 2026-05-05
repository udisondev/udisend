//go:build slow

package topotest_test

import (
	"testing"
	"time"
)

// Scale tier — N≥100 in-process peers. Run:
//
//	go test -tags=slow ./internal/topotest/...
//	go test -race -tags=slow ./internal/topotest/...
//
// Multi-seed bootstrap (5 seeds for N=100/500) keeps any single
// recv loop from becoming a hot spot, so these stay deterministic
// even under -race.

// TestScale_N100 — 100 peers in one process via 5-seed bootstrap.
func TestScale_N100(t *testing.T) {
	runScale(t, 100, 10*time.Second)
}

// TestScale_N500 — 500 peers, stress demonstration.
func TestScale_N500(t *testing.T) {
	runScale(t, 500, 30*time.Second)
}
