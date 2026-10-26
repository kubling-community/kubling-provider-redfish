package redfish

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxRetryDrainBytes = 64 * 1024

// RetryPolicy bounds retries for idempotent Redfish requests. MaxAttempts
// includes the initial request.
type RetryPolicy struct {
	MaxAttempts int
	MinBackoff  time.Duration
	MaxBackoff  time.Duration
}

// DefaultRetryPolicy returns the provider's conservative retry defaults.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 3,
		MinBackoff:  100 * time.Millisecond,
		MaxBackoff:  2 * time.Second,
	}
}

func normalizeRetryPolicy(policy RetryPolicy) (RetryPolicy, error) {
	defaults := DefaultRetryPolicy()
	if policy.MaxAttempts == 0 {
		policy.MaxAttempts = defaults.MaxAttempts
	}
	if policy.MinBackoff == 0 {
		policy.MinBackoff = defaults.MinBackoff
	}
	if policy.MaxBackoff == 0 {
		policy.MaxBackoff = defaults.MaxBackoff
	}
	if policy.MaxAttempts < 1 || policy.MinBackoff < 0 || policy.MaxBackoff < 0 || policy.MaxBackoff < policy.MinBackoff {
		return RetryPolicy{}, errors.New("invalid Redfish retry policy")
	}
	return policy, nil
}

type sleepFunc func(context.Context, time.Duration) error

type retryTransport struct {
	next   http.RoundTripper
	policy RetryPolicy
	sleep  sleepFunc
	now    func() time.Time
}

func (t *retryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		return t.next.RoundTrip(request)
	}

	for attempt := 1; attempt <= t.policy.MaxAttempts; attempt++ {
		response, err := t.next.RoundTrip(request.Clone(request.Context()))
		if !shouldRetry(request.Context(), response, err) || attempt == t.policy.MaxAttempts {
			return response, err
		}
		delay := t.backoff(attempt)
		if response != nil {
			if retryAfter, ok := parseRetryAfter(response.Header.Get("Retry-After"), t.now()); ok {
				delay = min(retryAfter, t.policy.MaxBackoff)
			}
			_, _ = io.CopyN(io.Discard, response.Body, maxRetryDrainBytes)
			_ = response.Body.Close()
		}
		if err := t.sleep(request.Context(), delay); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("unreachable retry state")
}

func (t *retryTransport) backoff(failedAttempt int) time.Duration {
	delay := t.policy.MinBackoff
	for step := 1; step < failedAttempt; step++ {
		if delay >= t.policy.MaxBackoff/2 {
			return t.policy.MaxBackoff
		}
		delay *= 2
	}
	return min(delay, t.policy.MaxBackoff)
}

func shouldRetry(ctx context.Context, response *http.Response, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		return retryableTransportError(err)
	}
	return response != nil && retryableStatus(response.StatusCode)
}

func retryableStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout,
		http.StatusTooEarly,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryableTransportError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || isTLSError(err) ||
		errors.Is(err, errUnsafeRedirect) || errors.Is(err, errTooManyRedirects) {
		return false
	}
	return true
}

func isTLSError(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	var recordHeader tls.RecordHeaderError
	return errors.As(err, &unknownAuthority) || errors.As(err, &hostname) ||
		errors.As(err, &invalidCertificate) || errors.As(err, &recordHeader)
}

func parseRetryAfter(raw string, now time.Time) (time.Duration, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(value, 10, 31); err == nil {
		return time.Duration(seconds) * time.Second, true
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	if !date.After(now) {
		return 0, true
	}
	return date.Sub(now), true
}

func sleepWithContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func timeNow() time.Time {
	return time.Now()
}
