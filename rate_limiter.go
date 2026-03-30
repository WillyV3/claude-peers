package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a simple sliding-window rate limiter keyed by IP.
type rateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
	limit   int
	window  time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		windows: make(map[string][]time.Time),
		limit:   limit,
		window:  window,
	}
	go rl.cleanup()
	return rl
}

// allow returns true if the request from ip is within the rate limit.
func (rl *rateLimiter) allow(ip string) bool {
	now := time.Now()
	cutoff := now.Add(-rl.window)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Prune old timestamps.
	ts := rl.windows[ip]
	valid := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.limit {
		rl.windows[ip] = valid
		return false
	}

	rl.windows[ip] = append(valid, now)
	return true
}

// cleanup removes stale entries every minute to prevent unbounded memory growth.
func (rl *rateLimiter) cleanup() {
	for range time.Tick(time.Minute) {
		cutoff := time.Now().Add(-rl.window)
		rl.mu.Lock()
		for ip, ts := range rl.windows {
			valid := ts[:0]
			for _, t := range ts {
				if t.After(cutoff) {
					valid = append(valid, t)
				}
			}
			if len(valid) == 0 {
				delete(rl.windows, ip)
			} else {
				rl.windows[ip] = valid
			}
		}
		rl.mu.Unlock()
	}
}

func writeRateLimited(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	json.NewEncoder(w).Encode(map[string]string{
		"code":  "RATE_LIMITED",
		"error": "too many requests",
	})
}
