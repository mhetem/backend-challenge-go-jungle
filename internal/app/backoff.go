package app

import (
	"math/rand/v2"
	"time"
)

func ExponentialBackoff(base, ceiling time.Duration) func(attempt int) time.Duration {
	return func(attempt int) time.Duration {
		d := base
		for i := 1; i < attempt && d < ceiling; i++ {
			d *= 2
		}
		d = min(d, ceiling)
		return d + rand.N(d/5+1)
	}
}
