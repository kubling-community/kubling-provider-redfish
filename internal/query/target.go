package query

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type targetEvent struct {
	target   device.Device
	tuples   []*providerv1.Tuple
	failure  *targetFailure
	complete bool
}

func (e *Executor) runTarget(
	queryContext context.Context,
	target device.Device,
	plan queryPlan,
	requestedBatchSize *uint32,
	events chan<- targetEvent,
	queryID string,
) {
	if !e.acquireSlot(queryContext) {
		return
	}
	defer e.releaseSlot()

	targetContext, cancel := context.WithTimeout(queryContext, e.config.PerDeviceTimeout)
	defer cancel()
	stream := newResultStream(
		targetContext,
		&target,
		plan,
		e.config,
		e.newClient,
		requestedBatchSize,
	)

	for {
		batch, err := stream.Next(targetContext)
		if errors.Is(err, io.EOF) {
			if closeErr := stream.Close(); closeErr != nil {
				failure := failureFromError(
					target,
					status.Error(codes.Internal, "close Redfish target resources failed"),
				)
				sendTargetEvent(queryContext, events, targetEvent{target: target, failure: failure})
				return
			}
			sendTargetEvent(queryContext, events, targetEvent{target: target, complete: true})
			return
		}
		if err != nil {
			closeErr := stream.Close()
			if closeErr != nil {
				e.logCleanupFailure(queryContext, queryID, target, closeErr)
			}
			if queryContext.Err() != nil {
				return
			}
			sendTargetEvent(queryContext, events, targetEvent{
				target:  target,
				failure: failureFromError(target, err),
			})
			return
		}
		if batch == nil || len(batch.GetTuples()) == 0 {
			_ = stream.Close()
			sendTargetEvent(queryContext, events, targetEvent{
				target: target,
				failure: failureFromError(
					target,
					status.Error(codes.Internal, "Redfish target returned an empty batch"),
				),
			})
			return
		}
		if !sendTargetEvent(queryContext, events, targetEvent{
			target: target,
			tuples: append([]*providerv1.Tuple(nil), batch.GetTuples()...),
		}) {
			_ = stream.Close()
			return
		}
	}
}

func (e *Executor) logCleanupFailure(
	ctx context.Context,
	queryID string,
	target device.Device,
	err error,
) {
	logger := e.config.Logger
	if logger == nil || err == nil {
		return
	}
	failure := failureFromError(target, sourceError("close Redfish session", err))
	logger.LogAttrs(
		ctx,
		slog.LevelWarn,
		"Redfish session cleanup failed",
		slog.String("provider", "redfish"),
		slog.String("query_id", queryID),
		slog.String("namespace", target.Namespace()),
		slog.String("device_id", target.ID()),
		slog.String("code", failure.warning.GetCode()),
		slog.String("grpc_code", failure.grpcCode.String()),
		slog.Bool("retryable", failure.warning.GetRetryable()),
	)
}

func sendTargetEvent(
	ctx context.Context,
	events chan<- targetEvent,
	event targetEvent,
) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}
