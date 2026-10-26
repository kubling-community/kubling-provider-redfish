package query

import (
	"context"
	"io"
	"sync"

	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/protobuf/proto"
)

// staticStream serves provider-local rows without opening a Redfish session.
// It follows the same bounded batching and outcome contract as remote streams.
type staticStream struct {
	mu        sync.Mutex
	fields    []*providerv1.Field
	tuples    []*providerv1.Tuple
	batchSize int
	index     int
	closed    bool
	outcome   *providerv1.QueryOutcome
}

func newStaticStream(
	fields []*providerv1.Field,
	tuples []*providerv1.Tuple,
	batchSize int,
) *staticStream {
	return &staticStream{
		fields:    fields,
		tuples:    tuples,
		batchSize: batchSize,
		outcome: &providerv1.QueryOutcome{
			Completion: providerv1.QueryCompletion_QUERY_COMPLETION_COMPLETE,
		},
	}
}

func (s *staticStream) Next(ctx context.Context) (*providerv1.TupleBatch, error) {
	if err := contextStatus(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.index >= len(s.tuples) {
		return nil, io.EOF
	}
	end := min(s.index+s.batchSize, len(s.tuples))
	batch := &providerv1.TupleBatch{
		Fields: s.fields,
		Tuples: append([]*providerv1.Tuple(nil), s.tuples[s.index:end]...),
	}
	s.index = end
	return batch, nil
}

func (s *staticStream) Outcome() *providerv1.QueryOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.Clone(s.outcome).(*providerv1.QueryOutcome)
}

func (s *staticStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.tuples = nil
	s.mu.Unlock()
	return nil
}

var (
	_ providersdk.ResultStream       = (*staticStream)(nil)
	_ providersdk.QueryOutcomeStream = (*staticStream)(nil)
)
