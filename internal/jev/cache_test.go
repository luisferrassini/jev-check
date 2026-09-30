package jev

import (
	"testing"
	"time"
)

func TestCacheFresh(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for age, want := range map[time.Duration]bool{
		0: true, time.Second: true, 24*time.Hour - time.Nanosecond: true,
		24 * time.Hour: false, 25 * time.Hour: false, -time.Nanosecond: false,
	} {
		if got := fresh(now.Add(-age), now); got != want {
			t.Errorf("age %v: fresh %v, want %v", age, got, want)
		}
	}
}
