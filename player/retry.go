package main

// retry.go keeps the old package-main names as thin aliases so call sites and
// tests do not change while the implementation lives in the cacheable leaf
// package internal/retry (ARCH-1).

import (
	"context"
	"net/http"
	"time"

	jhretry "jukahub/JukaHub/internal/retry"
)

type retryPolicy = jhretry.Policy

func defaultRetryPolicy() retryPolicy { return jhretry.DefaultPolicy() }

// Tests use this alias to check the backoff math directly.
func backoffDelay(attempt int, base, max time.Duration, jitter float64, rnd func() float64) time.Duration {
	return jhretry.BackoffDelay(attempt, base, max, jitter, rnd)
}

func retryableStatus(code int) bool { return jhretry.RetryableStatus(code) }

// Tests use this alias to check Retry-After parsing directly.
func retryAfterDelay(value string, now time.Time) time.Duration {
	return jhretry.RetryAfterDelay(value, now)
}

func doWithRetry(ctx context.Context, client *http.Client, req *http.Request, p retryPolicy) (*http.Response, error) {
	return jhretry.DoWithRetry(ctx, client, req, p)
}
