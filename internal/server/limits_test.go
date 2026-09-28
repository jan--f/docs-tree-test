package server

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func TestRateLimiterMixedWindowsAtCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newRateLimiter()
		for i := 0; i < 240; i++ {
			if _, ok := l.allow("hourly-join", 240, time.Hour); !ok {
				t.Fatal("hourly quota rejected a request before its limit")
			}
		}
		for i := 0; i < maxRateWindows-2; i++ {
			if _, ok := l.allow(fmt.Sprint(i), 30, time.Minute); !ok {
				t.Fatal("minute quota rejected a new key before capacity")
			}
		}
		l.allow("active-hourly", 2, time.Hour)
		if _, ok := l.allow("active-hourly", 2, time.Hour); !ok {
			t.Fatal("capacity blocked a known key with remaining budget")
		}
		if _, ok := l.allow("new-key", 30, time.Minute); ok {
			t.Fatal("cache exceeded its capacity")
		}

		time.Sleep(3 * time.Minute)
		// A minute-based request should reclaim expired minute entries without
		// resetting still-active hourly quotas, even after a quiet interval.
		if _, ok := l.allow("new-key", 30, time.Minute); !ok {
			t.Fatal("expired entries did not release capacity")
		}
		for _, quota := range []struct {
			key   string
			limit int
		}{{"hourly-join", 240}, {"active-hourly", 2}} {
			if wait, ok := l.allow(quota.key, quota.limit, time.Hour); ok || wait != 57*time.Minute {
				t.Fatalf("hourly quota %s lost its original expiry: allowed=%t wait=%v", quota.key, ok, wait)
			}
		}
		time.Sleep(57 * time.Minute)
		if _, ok := l.allow("hourly-join", 240, time.Hour); !ok {
			t.Fatal("hourly budget did not reset at its own expiry")
		}
	})
}
