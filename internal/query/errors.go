package query

import (
	"context"
	"errors"
	"net/http"

	"github.com/kubling-community/kubling-provider-redfish/internal/redfish"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type diagnosticError struct {
	err       error
	code      string
	retryable bool
}

func (e *diagnosticError) Error() string {
	return e.err.Error()
}

func (e *diagnosticError) Unwrap() error {
	return e.err
}

func (e *diagnosticError) GRPCStatus() *status.Status {
	return status.Convert(e.err)
}

func withDiagnostic(err error, code string, retryable bool) error {
	return &diagnosticError{err: err, code: code, retryable: retryable}
}

func diagnosticStatus(
	grpcCode codes.Code,
	diagnosticCode string,
	message string,
	retryable bool,
) error {
	return withDiagnostic(status.Error(grpcCode, message), diagnosticCode, retryable)
}

func sourceError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.FromContextError(context.Canceled).Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(context.DeadlineExceeded).Err()
	}
	if grpcStatus, ok := status.FromError(err); ok && grpcStatus.Code() != codes.Unknown {
		return status.Error(grpcStatus.Code(), operation+" failed")
	}
	var redfishError *redfish.Error
	if !errors.As(err, &redfishError) {
		return status.Error(codes.Internal, operation+" failed")
	}
	code := codes.Unavailable
	switch redfishError.Kind() {
	case redfish.ErrorKindConfiguration, redfish.ErrorKindClosed:
		code = codes.FailedPrecondition
	case redfish.ErrorKindAuthentication:
		if redfishError.StatusCode() == http.StatusForbidden {
			code = codes.PermissionDenied
		} else {
			code = codes.Unauthenticated
		}
	case redfish.ErrorKindUnsafeURI, redfish.ErrorKindRedirect, redfish.ErrorKindInvalidResponse:
		code = codes.DataLoss
	case redfish.ErrorKindResponseLimit:
		code = codes.ResourceExhausted
	case redfish.ErrorKindHTTP:
		switch redfishError.StatusCode() {
		case http.StatusNotFound:
			code = codes.NotFound
		case http.StatusTooManyRequests:
			code = codes.ResourceExhausted
		default:
			if redfishError.StatusCode() < http.StatusInternalServerError {
				code = codes.FailedPrecondition
			}
		}
	case redfish.ErrorKindTLS, redfish.ErrorKindTransport:
		code = codes.Unavailable
	}
	diagnosticCode, retryable := redfishDiagnostic(redfishError)
	return withDiagnostic(
		status.Errorf(code, "%s: %v", operation, redfishError),
		diagnosticCode,
		retryable,
	)
}

func redfishDiagnostic(redfishError *redfish.Error) (string, bool) {
	switch redfishError.Kind() {
	case redfish.ErrorKindAuthentication:
		if redfishError.StatusCode() == http.StatusForbidden {
			return "REDFISH_AUTH_PERMISSION_DENIED", false
		}
		return "REDFISH_AUTH_UNAUTHENTICATED", false
	case redfish.ErrorKindTLS:
		return "REDFISH_CONNECT_TLS", false
	case redfish.ErrorKindUnsafeURI:
		return "REDFISH_RESOURCE_UNSAFE_URI", false
	case redfish.ErrorKindRedirect:
		return "REDFISH_REDIRECT_REJECTED", false
	case redfish.ErrorKindInvalidResponse:
		return "REDFISH_RESOURCE_INVALID_RESPONSE", false
	case redfish.ErrorKindResponseLimit:
		return "REDFISH_RESPONSE_LIMIT_EXCEEDED", false
	case redfish.ErrorKindConfiguration, redfish.ErrorKindClosed:
		return "REDFISH_TARGET_FAILED_PRECONDITION", false
	case redfish.ErrorKindHTTP:
		switch redfishError.StatusCode() {
		case http.StatusNotFound:
			return "REDFISH_RESOURCE_NOT_FOUND", false
		case http.StatusTooManyRequests:
			return "REDFISH_TARGET_RATE_LIMITED", true
		default:
			if redfishError.StatusCode() >= http.StatusInternalServerError {
				return "REDFISH_TARGET_UNAVAILABLE", redfishError.Retryable()
			}
			return "REDFISH_TARGET_HTTP_ERROR", redfishError.Retryable()
		}
	case redfish.ErrorKindTransport:
		return "REDFISH_TARGET_UNAVAILABLE", redfishError.Retryable()
	default:
		return "REDFISH_TARGET_INTERNAL", false
	}
}

func contextStatus(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	return nil
}
