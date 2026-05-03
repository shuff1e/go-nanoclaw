package brain

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"
)

// RetryTransport wraps an http.Transport with retry logic for transient errors.
type RetryTransport struct {
	Base      http.RoundTripper
	MaxRetry  int
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

func NewRetryTransport() *RetryTransport {
	return &RetryTransport{
		Base: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		},
		MaxRetry:  3,
		BaseDelay: 500 * time.Millisecond,
		MaxDelay:  30 * time.Second,
	}
}

func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	var lastErr error
	for attempt := 0; attempt <= t.MaxRetry; attempt++ {
		if attempt > 0 {
			delay := t.retryDelay(attempt)
			slog.Warn("Retrying HTTP request",
				"attempt", attempt,
				"delay", delay,
				"url", req.URL.String(),
			)
			select {
			case <-time.After(delay):
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}

		// Clone body for retry (body is consumed on each attempt)
		var bodyClone []byte
		if req.Body != nil {
			var err error
			bodyClone, err = io.ReadAll(req.Body)
			req.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("read request body: %w", err)
			}
		}

		// Restore body for this attempt
		if bodyClone != nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyClone))
			req.ContentLength = int64(len(bodyClone))
		}

		resp, err := base.RoundTrip(req)
		if err != nil {
			lastErr = err
			if !isRetryableError(err) {
				return nil, err
			}
			continue
		}

		if isRetryableStatus(resp.StatusCode) {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			// Respect Retry-After header
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil {
					t.MaxDelay = time.Duration(secs) * time.Second
				}
			}
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("max retries (%d) exceeded: %w", t.MaxRetry, lastErr)
}

func (t *RetryTransport) retryDelay(attempt int) time.Duration {
	delay := float64(t.BaseDelay) * math.Pow(2, float64(attempt-1))
	if delay > float64(t.MaxDelay) {
		delay = float64(t.MaxDelay)
	}
	// Add jitter: ±25%
	jitter := delay * 0.25
	delay = delay - jitter + (float64(time.Now().UnixNano()%1000)/1000)*2*jitter
	return time.Duration(delay)
}

func isRetryableStatus(code int) bool {
	return code == 429 || code == 502 || code == 503 || code == 504
}

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if err == context.DeadlineExceeded || err == context.Canceled {
		return false
	}
	// Network-level errors are generally retryable
	return true
}
