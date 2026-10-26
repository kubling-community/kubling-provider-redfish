package query

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type coordinatorStream struct {
	mu sync.Mutex

	parent             context.Context
	executor           *Executor
	plan               queryPlan
	targets            []device.Device
	fields             []*providerv1.Field
	requestedBatchSize *uint32
	batchSize          int
	allowPartial       bool
	queryID            string
	startedAt          time.Time
	finishLogged       bool

	started    bool
	ctx        context.Context
	cancel     context.CancelFunc
	events     <-chan targetEvent
	done       chan struct{}
	unfinished map[string]device.Device
	pending    []*providerv1.Tuple
	rowCount   int
	warnings   []*providerv1.QueryWarning
	partial    bool
	outcome    *providerv1.QueryOutcome
	terminal   error
	closed     bool
}

func newCoordinatorStream(
	parent context.Context,
	request *providerv1.QueryRequest,
	plan queryPlan,
	targets []device.Device,
	executor *Executor,
	queryID string,
) *coordinatorStream {
	var requestedBatchSize *uint32
	if request.BatchSize != nil {
		value := request.GetBatchSize()
		requestedBatchSize = &value
	}
	stream := &coordinatorStream{
		parent:             parent,
		executor:           executor,
		plan:               plan,
		targets:            append([]device.Device(nil), targets...),
		fields:             plan.fields(),
		requestedBatchSize: requestedBatchSize,
		batchSize:          executor.config.batchSize(requestedBatchSize),
		allowPartial:       request.GetAllowPartialResults(),
		queryID:            queryID,
		startedAt:          time.Now(),
		done:               make(chan struct{}),
	}
	stream.logStarted()
	return stream
}

func (s *coordinatorStream) startLocked() {
	if s.started {
		return
	}
	s.started = true
	s.unfinished = make(map[string]device.Device, len(s.targets))
	for _, target := range s.targets {
		s.unfinished[targetKey(target)] = target
	}
	if len(s.targets) == 0 {
		close(s.done)
		return
	}

	s.ctx, s.cancel = context.WithTimeout(s.parent, s.executor.config.TotalTimeout)
	events := make(chan targetEvent)
	jobs := make(chan device.Device)
	s.events = events
	workerCount := min(s.executor.config.MaxConcurrentDevices, len(s.targets))

	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for target := range jobs {
				s.executor.runTarget(
					s.ctx,
					target,
					s.plan,
					s.requestedBatchSize,
					events,
					s.queryID,
				)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, target := range s.targets {
			select {
			case jobs <- target:
			case <-s.ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(events)
		close(s.done)
	}()
}

func (s *coordinatorStream) Next(callContext context.Context) (*providerv1.TupleBatch, error) {
	if err := contextStatus(callContext); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, io.EOF
	}
	s.startLocked()

	for {
		if s.closed {
			return nil, io.EOF
		}
		if len(s.pending) > 0 {
			count := min(s.batchSize, len(s.pending))
			tuples := append([]*providerv1.Tuple(nil), s.pending[:count]...)
			s.pending = s.pending[count:]
			return &providerv1.TupleBatch{Fields: s.fields, Tuples: tuples}, nil
		}
		if s.terminal != nil {
			s.logFinishedLocked("failed", s.terminal)
			return nil, s.terminal
		}
		if len(s.unfinished) == 0 {
			s.finishOutcomeLocked()
			return nil, io.EOF
		}

		events := s.events
		queryContext := s.ctx
		s.mu.Unlock()
		var event targetEvent
		var open bool
		select {
		case event, open = <-events:
		case <-callContext.Done():
			s.mu.Lock()
			return nil, contextStatus(callContext)
		case <-queryContext.Done():
			s.mu.Lock()
			s.handleQueryContextDoneLocked()
			continue
		}
		s.mu.Lock()

		if !open {
			if len(s.unfinished) != 0 && s.terminal == nil {
				s.terminal = status.Error(codes.Internal, "Redfish query workers stopped before every target completed")
			}
			continue
		}
		s.handleTargetEventLocked(event)
	}
}

func (s *coordinatorStream) handleTargetEventLocked(event targetEvent) {
	key := targetKey(event.target)
	if _, exists := s.unfinished[key]; !exists {
		s.failInternalLocked("Redfish query worker returned a duplicate or unknown target event")
		return
	}
	kinds := 0
	if len(event.tuples) > 0 {
		kinds++
	}
	if event.failure != nil {
		kinds++
	}
	if event.complete {
		kinds++
	}
	if kinds != 1 {
		s.failInternalLocked("Redfish query worker returned an invalid target event")
		return
	}

	if len(event.tuples) > 0 {
		s.acceptTuplesLocked(event.tuples)
		return
	}
	delete(s.unfinished, key)
	if event.complete {
		s.logTargetCompleted(event.target)
		return
	}
	s.logTargetFailure(event.target, event.failure)
	if !s.allowPartial {
		failure := event.failure
		s.terminal = status.Errorf(
			failure.grpcCode,
			"Redfish target %q failed: %s",
			failure.warning.GetTarget(),
			failure.warning.GetMessage(),
		)
		s.cancelQueryLocked()
		return
	}
	s.partial = true
	s.warnings = append(s.warnings, event.failure.warning)
}

func (s *coordinatorStream) acceptTuplesLocked(
	tuples []*providerv1.Tuple,
) {
	remaining := s.executor.config.MaxRowsPerQuery - s.rowCount
	if len(tuples) <= remaining {
		s.pending = append(s.pending, tuples...)
		s.rowCount += len(tuples)
		return
	}
	if !s.allowPartial {
		s.terminal = status.Errorf(
			codes.ResourceExhausted,
			"Redfish query exceeded the configured total row limit of %d",
			s.executor.config.MaxRowsPerQuery,
		)
		s.cancelQueryLocked()
		return
	}
	if remaining > 0 {
		s.pending = append(s.pending, tuples[:remaining]...)
		s.rowCount += remaining
	}
	s.partial = true
	s.addFailureForUnfinishedLocked(func(incomplete device.Device) *targetFailure {
		return queryLimitFailure(incomplete, s.executor.config.MaxRowsPerQuery)
	})
	s.cancelQueryLocked()
}

func (s *coordinatorStream) handleQueryContextDoneLocked() {
	if s.parent.Err() != nil {
		s.terminal = status.FromContextError(s.parent.Err()).Err()
		s.cancelQueryLocked()
		return
	}
	if s.ctx != nil && s.ctx.Err() == context.DeadlineExceeded {
		if !s.allowPartial {
			for _, target := range s.unfinished {
				s.logTargetFailure(target, queryTimeoutFailure(target))
			}
			s.terminal = status.Error(codes.DeadlineExceeded, "Redfish query exceeded its total deadline")
			s.cancelQueryLocked()
			return
		}
		s.partial = true
		s.addFailureForUnfinishedLocked(queryTimeoutFailure)
		s.cancelQueryLocked()
		return
	}
	if !s.closed && s.terminal == nil && len(s.unfinished) != 0 {
		s.terminal = status.Error(codes.Internal, "Redfish query stopped before every target completed")
	}
}

func (s *coordinatorStream) addFailureForUnfinishedLocked(
	create func(device.Device) *targetFailure,
) {
	for key, target := range s.unfinished {
		failure := create(target)
		s.warnings = append(s.warnings, failure.warning)
		s.logTargetFailure(target, failure)
		delete(s.unfinished, key)
	}
}

func (s *coordinatorStream) failInternalLocked(message string) {
	s.terminal = status.Error(codes.Internal, message)
	s.cancelQueryLocked()
}

func (s *coordinatorStream) cancelQueryLocked() {
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *coordinatorStream) finishOutcomeLocked() {
	if s.outcome != nil {
		return
	}
	completion := providerv1.QueryCompletion_QUERY_COMPLETION_COMPLETE
	if s.partial {
		completion = providerv1.QueryCompletion_QUERY_COMPLETION_PARTIAL
	}
	s.outcome = &providerv1.QueryOutcome{
		Completion: completion,
		Warnings:   sortedWarnings(s.warnings),
	}
	result := "complete"
	if s.partial {
		result = "partial"
	}
	s.logFinishedLocked(result, nil)
	s.cancelQueryLocked()
}

func (s *coordinatorStream) Outcome() *providerv1.QueryOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outcome == nil {
		return nil
	}
	return proto.Clone(s.outcome).(*providerv1.QueryOutcome)
}

func (s *coordinatorStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	started := s.started
	s.cancelQueryLocked()
	s.logFinishedLocked("closed", nil)
	s.mu.Unlock()
	if started {
		<-s.done
	}
	return nil
}

func (s *coordinatorStream) logStarted() {
	logger := s.executor.config.Logger
	if logger == nil {
		return
	}
	logger.LogAttrs(
		s.parent,
		slog.LevelInfo,
		"Redfish query started",
		slog.String("provider", "redfish"),
		slog.String("query_id", s.queryID),
		slog.String("entity", s.plan.table.Name),
		slog.Int("target_count", len(s.targets)),
		slog.Bool("allow_partial_results", s.allowPartial),
	)
}

func (s *coordinatorStream) logTargetCompleted(target device.Device) {
	logger := s.executor.config.Logger
	if logger == nil {
		return
	}
	logger.LogAttrs(
		s.parent,
		slog.LevelDebug,
		"Redfish query target completed",
		slog.String("provider", "redfish"),
		slog.String("query_id", s.queryID),
		slog.String("namespace", target.Namespace()),
		slog.String("device_id", target.ID()),
	)
}

func (s *coordinatorStream) logTargetFailure(
	target device.Device,
	failure *targetFailure,
) {
	logger := s.executor.config.Logger
	if logger == nil || failure == nil || failure.warning == nil {
		return
	}
	logger.LogAttrs(
		s.parent,
		slog.LevelWarn,
		"Redfish query target failed",
		slog.String("provider", "redfish"),
		slog.String("query_id", s.queryID),
		slog.String("namespace", target.Namespace()),
		slog.String("device_id", target.ID()),
		slog.String("code", failure.warning.GetCode()),
		slog.String("grpc_code", failure.grpcCode.String()),
		slog.Bool("retryable", failure.warning.GetRetryable()),
	)
}

func (s *coordinatorStream) logFinishedLocked(result string, err error) {
	if s.finishLogged {
		return
	}
	s.finishLogged = true
	logger := s.executor.config.Logger
	if logger == nil {
		return
	}
	attributes := []slog.Attr{
		slog.String("provider", "redfish"),
		slog.String("query_id", s.queryID),
		slog.String("entity", s.plan.table.Name),
		slog.String("result", result),
		slog.Int("output_rows", s.rowCount),
		slog.Int("warning_count", len(s.warnings)),
		slog.Duration("duration", time.Since(s.startedAt)),
	}
	if err != nil {
		attributes = append(attributes, slog.String("grpc_code", status.Code(err).String()))
	}
	logger.LogAttrs(s.parent, slog.LevelInfo, "Redfish query finished", attributes...)
}

var (
	_ providersdk.ResultStream       = (*coordinatorStream)(nil)
	_ providersdk.QueryOutcomeStream = (*coordinatorStream)(nil)
)
