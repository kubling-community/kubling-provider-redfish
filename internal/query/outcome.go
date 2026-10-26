package query

import (
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const maxDiagnosticRunes = 512

type targetFailure struct {
	warning  *providerv1.QueryWarning
	grpcCode codes.Code
}

func failureFromError(target device.Device, err error) *targetFailure {
	grpcCode := status.Code(err)
	code := "REDFISH_TARGET_INTERNAL"
	retryable := false
	var diagnostic *diagnosticError
	if errors.As(err, &diagnostic) {
		code = diagnostic.code
		retryable = diagnostic.retryable
		return &targetFailure{
			warning: partialCause(
				target,
				code,
				safeDiagnosticMessage(err, "Redfish target did not complete the query"),
				retryable,
			),
			grpcCode: grpcCode,
		}
	}
	switch grpcCode {
	case codes.Canceled:
		code = "REDFISH_TARGET_CANCELLED"
		retryable = true
	case codes.DeadlineExceeded:
		code = "REDFISH_TARGET_TIMEOUT"
		retryable = true
	case codes.Unauthenticated:
		code = "REDFISH_AUTH_UNAUTHENTICATED"
	case codes.PermissionDenied:
		code = "REDFISH_AUTH_PERMISSION_DENIED"
	case codes.NotFound:
		code = "REDFISH_RESOURCE_NOT_FOUND"
	case codes.ResourceExhausted:
		code = "REDFISH_RESULT_LIMIT_EXCEEDED"
	case codes.FailedPrecondition:
		code = "REDFISH_TARGET_FAILED_PRECONDITION"
	case codes.DataLoss:
		code = "REDFISH_RESOURCE_INVALID_RESPONSE"
	case codes.Unavailable:
		code = "REDFISH_TARGET_UNAVAILABLE"
		retryable = true
	case codes.InvalidArgument:
		code = "REDFISH_QUERY_INVALID"
	case codes.Internal, codes.Unknown, codes.OK:
		grpcCode = codes.Internal
	}
	return &targetFailure{
		warning: partialCause(
			target,
			code,
			safeDiagnosticMessage(err, "Redfish target did not complete the query"),
			retryable,
		),
		grpcCode: grpcCode,
	}
}

func queryLimitFailure(target device.Device, maximum int) *targetFailure {
	return &targetFailure{
		warning: partialCause(
			target,
			"REDFISH_RESULT_LIMIT_EXCEEDED",
			"Redfish query exceeded the configured total row limit of "+strconv.Itoa(maximum),
			false,
		),
		grpcCode: codes.ResourceExhausted,
	}
}

func queryTimeoutFailure(target device.Device) *targetFailure {
	return &targetFailure{
		warning: partialCause(
			target,
			"REDFISH_QUERY_TIMEOUT",
			"Redfish query exceeded its total deadline before this target completed",
			true,
		),
		grpcCode: codes.DeadlineExceeded,
	}
}

func partialCause(
	target device.Device,
	code string,
	message string,
	retryable bool,
) *providerv1.QueryWarning {
	return &providerv1.QueryWarning{
		Code:      code,
		Message:   boundedMessage(message),
		Target:    diagnosticTarget(target),
		Retryable: retryable,
		Role:      providerv1.QueryDiagnosticRole_QUERY_DIAGNOSTIC_ROLE_PARTIAL_RESULT_CAUSE,
	}
}

func diagnosticTarget(target device.Device) string {
	return "namespace=" + url.QueryEscape(target.Namespace()) +
		"&device_id=" + url.QueryEscape(target.ID())
}

func targetKey(target device.Device) string {
	return target.Namespace() + "\x00" + target.ID()
}

func safeDiagnosticMessage(err error, fallback string) string {
	message := ""
	if err != nil {
		message = status.Convert(err).Message()
	}
	if strings.TrimSpace(message) == "" {
		message = fallback
	}
	return boundedMessage(message)
}

func boundedMessage(message string) string {
	runes := []rune(strings.TrimSpace(message))
	if len(runes) == 0 {
		return "Redfish target did not complete the query"
	}
	if len(runes) > maxDiagnosticRunes {
		runes = runes[:maxDiagnosticRunes]
	}
	return string(runes)
}

func sortedWarnings(warnings []*providerv1.QueryWarning) []*providerv1.QueryWarning {
	result := make([]*providerv1.QueryWarning, 0, len(warnings))
	for _, warning := range warnings {
		if warning != nil {
			result = append(result, proto.Clone(warning).(*providerv1.QueryWarning))
		}
	}
	sort.Slice(result, func(left int, right int) bool {
		if result[left].GetTarget() != result[right].GetTarget() {
			return result[left].GetTarget() < result[right].GetTarget()
		}
		if result[left].GetCode() != result[right].GetCode() {
			return result[left].GetCode() < result[right].GetCode()
		}
		return result[left].GetMessage() < result[right].GetMessage()
	})
	return result
}
