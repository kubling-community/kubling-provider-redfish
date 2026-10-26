package query

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type resultStream struct {
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	target     *device.Device
	plan       queryPlan
	config     Config
	factory    clientFactory
	batchSize  int
	client     sourceClient
	walker     *resourceWalker
	pendingErr error
	closeErr   error
	closed     bool
	done       bool
}

func newResultStream(
	ctx context.Context,
	target *device.Device,
	plan queryPlan,
	config Config,
	factory clientFactory,
	requestedBatchSize *uint32,
) *resultStream {
	streamContext, cancel := context.WithCancel(ctx)
	return &resultStream{
		ctx:       streamContext,
		cancel:    cancel,
		target:    target,
		plan:      plan,
		config:    config,
		factory:   factory,
		batchSize: config.batchSize(requestedBatchSize),
		done:      target == nil,
	}
}

func (s *resultStream) Next(ctx context.Context) (*providerv1.TupleBatch, error) {
	if err := contextStatus(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingErr != nil {
		err := s.pendingErr
		s.pendingErr = nil
		s.done = true
		return nil, err
	}
	if s.closed || s.done {
		return nil, io.EOF
	}
	operationContext, release := combinedContext(s.ctx, ctx)
	defer release()
	if err := contextStatus(operationContext); err != nil {
		return nil, err
	}
	if s.client == nil {
		client, err := s.factory(operationContext, *s.target, s.config.clientOptions())
		if err != nil {
			s.done = true
			return nil, sourceError("connect to Redfish device", err)
		}
		if client == nil {
			s.done = true
			return nil, status.Error(codes.Internal, "Redfish query client factory returned nil")
		}
		s.client = client
		s.walker = newResourceWalker(
			client,
			s.plan.table,
			s.plan.route.resourceURI,
			s.config.MaxPagesPerCollection,
			s.config.MaxRowsPerDevice,
		)
	}

	tuples := make([]*providerv1.Tuple, 0, s.batchSize)
	for len(tuples) < s.batchSize {
		row, err := s.walker.next(operationContext)
		if errors.Is(err, io.EOF) {
			s.done = true
			if closeErr := s.closeClient(); closeErr != nil {
				closeErr = sourceError("close Redfish session", closeErr)
				if len(tuples) == 0 {
					return nil, closeErr
				}
				s.pendingErr = closeErr
			}
			break
		}
		if err != nil {
			_ = s.closeClient()
			if len(tuples) == 0 {
				s.done = true
				return nil, err
			}
			s.pendingErr = err
			break
		}
		tuple, err := projectRow(s.plan, *s.target, row)
		if err != nil {
			_ = s.closeClient()
			projectionErr := status.Error(codes.DataLoss, "project Redfish resource: "+err.Error())
			if len(tuples) == 0 {
				s.done = true
				return nil, projectionErr
			}
			s.pendingErr = projectionErr
			break
		}
		tuples = append(tuples, tuple)
	}
	if len(tuples) == 0 {
		return nil, io.EOF
	}
	return &providerv1.TupleBatch{Fields: s.plan.fields(), Tuples: tuples}, nil
}

func (s *resultStream) Close() error {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	s.done = true
	return s.closeClient()
}

func (s *resultStream) closeClient() error {
	if s.client == nil {
		return s.closeErr
	}
	s.closeErr = s.client.Close()
	s.client = nil
	s.walker = nil
	return s.closeErr
}

func combinedContext(base context.Context, call context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(base)
	stop := context.AfterFunc(call, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

var _ providersdk.ResultStream = (*resultStream)(nil)
