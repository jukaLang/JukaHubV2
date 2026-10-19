package main

// Bounded retries with exponential backoff and jitter.
//
// The handheld runs on flaky Wi-Fi and the update feed is the one request that
// must survive a transient 502. Callers still own timeouts and response size
// limits; this helper only decides whether a failed GET is worth another try.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// retryPolicy controls bounded retries. Attempts counts the first try, so
// Attempts <= 1 disables retrying.
type retryPolicy struct {
	Attempts  int
	BaseDelay time.Duration
	MaxDelay  time.Duration
	Jitter    float64 // 0..1 fraction of the delay, added or subtracted at random
}

// defaultRetryPolicy is tuned for a background metadata fetch: three tries,
// roughly 0.3s then 0.6s of waiting, never more than four seconds.
func defaultRetryPolicy() retryPolicy {
	return retryPolicy{
		Attempts:  3,
		BaseDelay: 300 * time.Millisecond,
		MaxDelay:  4 * time.Second,
		Jitter:    0.3,
	}
}

// backoffDelay returns the wait after the given failed attempt (1-based).
// Jitter spreads retries from many devices so an outage does not turn into a
// synchronised stampede when the service recovers.
func backoffDelay(attempt int, base, max time.Duration, jitter float64, rnd func() float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if base <= 0 {
		return 0
	}
	d := base
	for i := 1; i < attempt && (max <= 0 || d < max); i++ {
		d *= 2
	}
	if max > 0 && d > max {
		d = max
	}
	if jitter > 0 && rnd != nil {
		if jitter > 1 {
			jitter = 1
		}
		d += time.Duration((rnd()*2 - 1) * jitter * float64(d))
		if d < 0 {
			d = 0
		}
	}
	return d
}

// retryableStatus reports whether an HTTP status is worth retrying. Client
// errors other than timeout/rate limiting are permanent, so they are returned
// to the caller immediately.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	}
	return code >= 500
}

// retryAfterDelay parses a Retry-After header (seconds or HTTP date). A
// malformed or past value yields 0, meaning "use the normal backoff".
func retryAfterDelay(value string, now time.Time) time.Duration {
	v := strings.TrimSpace(value)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// doWithRetry executes req, retrying transient failures. It returns the last
// non-retryable response untouched (including error statuses) so callers keep
// their existing status reporting. Retryable responses have their bodies
// drained and closed before the next attempt.
func doWithRetry(ctx context.Context, client *http.Client, req *http.Request, p retryPolicy) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("retry: nil request")
	}
	if client == nil {
		client = http.DefaultClient
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if p.Attempts < 1 {
		p.Attempts = 1
	}
	req = req.WithContext(ctx)
	// A body that cannot be rewound (no GetBody) makes a retry unsafe.
	rewindable := req.Body == nil || req.GetBody != nil

	var lastErr error
	var serverWait time.Duration
	for attempt := 1; attempt <= p.Attempts; attempt++ {
		if attempt > 1 {
			if !rewindable {
				break
			}
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, fmt.Errorf("retry: rewinding request body: %w", err)
				}
				req.Body = body
			}
			wait := serverWait
			if wait <= 0 {
				wait = backoffDelay(attempt-1, p.BaseDelay, p.MaxDelay, p.Jitter, rand.Float64)
			}
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-timer.C:
				}
			}
		}
		serverWait = 0

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}
		if !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, req.URL.Redacted())
		serverWait = retryAfterDelay(resp.Header.Get("Retry-After"), time.Now())
		if p.MaxDelay > 0 && serverWait > p.MaxDelay {
			serverWait = p.MaxDelay
		}
		// Drain a little so the connection can be reused, then close.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}

	if lastErr == nil {
		lastErr = errors.New("retry: request did not run")
	}
	return nil, fmt.Errorf("request failed after retry: %w", lastErr)
}
