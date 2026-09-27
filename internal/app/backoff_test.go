package app_test

import (
	"testing"
	"time"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
)

func TestExponentialBackoff(t *testing.T) {
	backoff := app.ExponentialBackoff(time.Second, time.Minute)
	tests := []struct {
		attempt int
		base    time.Duration
	}{
		{0, time.Second},
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{6, 32 * time.Second},
		{7, time.Minute},
		{20, time.Minute},
		{1000, time.Minute},
	}
	for _, tt := range tests {
		for range 100 {
			if d := backoff(tt.attempt); d < tt.base || d > tt.base+tt.base/5 {
				t.Fatalf("backoff(%d) = %s; want within [%s, %s]", tt.attempt, d, tt.base, tt.base+tt.base/5)
			}
		}
	}
}
