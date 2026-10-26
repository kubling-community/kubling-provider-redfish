package redfish

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/stmcginnis/gofish/schemas"
)

// ErrorKind classifies adapter failures without exposing response bodies,
// credentials or query strings.
type ErrorKind string

const (
	ErrorKindConfiguration   ErrorKind = "configuration"
	ErrorKindTLS             ErrorKind = "tls"
	ErrorKindAuthentication  ErrorKind = "authentication"
	ErrorKindHTTP            ErrorKind = "http"
	ErrorKindTransport       ErrorKind = "transport"
	ErrorKindUnsafeURI       ErrorKind = "unsafe_uri"
	ErrorKindRedirect        ErrorKind = "redirect"
	ErrorKindResponseLimit   ErrorKind = "response_limit"
	ErrorKindInvalidResponse ErrorKind = "invalid_response"
	ErrorKindClosed          ErrorKind = "closed"
)

// Error is a sanitized Redfish adapter error.
type Error struct {
	operation  string
	kind       ErrorKind
	statusCode int
	retryable  bool
	cause      error
}

// Kind returns the stable failure classification.
func (e *Error) Kind() ErrorKind {
	return e.kind
}

// StatusCode returns an HTTP status when the service supplied one.
func (e *Error) StatusCode() int {
	return e.statusCode
}

// Retryable reports whether repeating an idempotent request may succeed.
func (e *Error) Retryable() bool {
	return e.retryable
}

// Error returns a bounded message without the source error text.
func (e *Error) Error() string {
	if e.statusCode != 0 {
		return fmt.Sprintf("Redfish %s failed (%s, HTTP %d)", e.operation, e.kind, e.statusCode)
	}
	return fmt.Sprintf("Redfish %s failed (%s)", e.operation, e.kind)
}

// GoString uses the same sanitized representation as Error.
func (e *Error) GoString() string {
	return e.Error()
}

// Unwrap preserves programmatic cancellation and safe sentinel inspection
// without including the cause in the formatted error.
func (e *Error) Unwrap() error {
	return e.cause
}

func newError(operation string, kind ErrorKind, statusCode int, retryable bool, cause error) *Error {
	return &Error{
		operation:  operation,
		kind:       kind,
		statusCode: statusCode,
		retryable:  retryable,
		cause:      cause,
	}
}

func normalizeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var existing *Error
	if errors.As(err, &existing) {
		return existing
	}
	statusCode := errorStatusCode(err)
	if statusCode != 0 {
		kind := ErrorKindHTTP
		if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
			kind = ErrorKindAuthentication
		}
		return newError(operation, kind, statusCode, retryableStatus(statusCode), nil)
	}
	if errors.Is(err, errResponseTooLarge) {
		return newError(operation, ErrorKindResponseLimit, 0, false, errResponseTooLarge)
	}
	if errors.Is(err, errSessionUnsupported) {
		return newError(operation, ErrorKindAuthentication, 0, false, errSessionUnsupported)
	}
	if errors.Is(err, errInvalidAuthMode) {
		return newError(operation, ErrorKindConfiguration, 0, false, errInvalidAuthMode)
	}
	if errors.Is(err, errUnsafeRedirect) || errors.Is(err, errTooManyRedirects) {
		if errors.Is(err, errTooManyRedirects) {
			return newError(operation, ErrorKindRedirect, 0, false, errTooManyRedirects)
		}
		return newError(operation, ErrorKindRedirect, 0, false, errUnsafeRedirect)
	}
	if errors.Is(err, errUnsafeURI) {
		return newError(operation, ErrorKindUnsafeURI, 0, false, errUnsafeURI)
	}
	if errors.Is(err, errInvalidJSON) {
		return newError(operation, ErrorKindInvalidResponse, 0, false, errInvalidJSON)
	}
	if errors.Is(err, errInvalidSessionLocation) {
		return newError(operation, ErrorKindInvalidResponse, 0, false, errInvalidSessionLocation)
	}
	if isTLSError(err) {
		return newError(operation, ErrorKindTLS, 0, false, nil)
	}
	if errors.Is(err, context.Canceled) {
		return newError(operation, ErrorKindTransport, 0, false, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return newError(operation, ErrorKindTransport, 0, false, context.DeadlineExceeded)
	}
	return newError(operation, ErrorKindTransport, 0, retryableTransportError(err), nil)
}

func errorStatusCode(err error) int {
	var redfishError *schemas.Error
	if errors.As(err, &redfishError) {
		return redfishError.HTTPReturnedStatusCode
	}
	return 0
}

var (
	errClientClosed           = errors.New("Redfish client is closed")
	errInvalidAuthMode        = errors.New("invalid Redfish authentication mode")
	errSessionUnsupported     = errors.New("Redfish session authentication is unsupported")
	errResponseTooLarge       = errors.New("Redfish response exceeds configured limit")
	errInvalidJSON            = errors.New("Redfish response is not valid JSON")
	errInvalidSessionLocation = errors.New("Redfish session response has no safe location")
)
