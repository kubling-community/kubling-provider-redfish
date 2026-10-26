package query

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	"github.com/kubling-community/kubling-provider-redfish/internal/redfish"
	"github.com/kubling-community/kubling-provider-redfish/internal/testredfish"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCoordinatorUsesOneSharedConcurrencyLimitAcrossQueries(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan string, 12)
	executor := coordinatorTestExecutor(t, Config{
		AllowUnboundedFanout: true,
		MaxDevicesPerQuery:   6,
		MaxConcurrentDevices: 2,
		PerDeviceTimeout:     5 * time.Second,
		TotalTimeout:         5 * time.Second,
	}, func(ctx context.Context, target device.Device, _ redfish.Options) (sourceClient, error) {
		current := active.Add(1)
		defer active.Add(-1)
		updateMaximum(&maximum, current)
		started <- target.Identity()
		<-ctx.Done()
		return nil, ctx.Err()
	})
	snapshot := querySnapshot(t,
		standaloneQueryDevice(t, "dc-a", "bmc-01"),
		standaloneQueryDevice(t, "dc-a", "bmc-02"),
		standaloneQueryDevice(t, "dc-a", "bmc-03"),
	)

	contexts := make([]context.Context, 2)
	cancels := make([]context.CancelFunc, 2)
	streams := make([]providersdk.ResultStream, 2)
	results := make(chan error, 2)
	for index := range streams {
		contexts[index], cancels[index] = context.WithCancel(context.Background())
		var err error
		streams[index], err = executor.Query(contexts[index], snapshot, queryRequest("SYSTEMS", 1))
		if err != nil {
			t.Fatalf("Query(%d) error = %v", index, err)
		}
		stream := streams[index]
		go func() {
			_, err := stream.Next(context.Background())
			results <- err
		}()
	}

	for index := 0; index < 2; index++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for shared query workers")
		}
	}
	select {
	case identity := <-started:
		t.Fatalf("third target %q started beyond the shared concurrency limit", identity)
	case <-time.After(100 * time.Millisecond):
	}
	if maximum.Load() != 2 {
		t.Fatalf("maximum active targets = %d, want 2", maximum.Load())
	}

	for _, cancel := range cancels {
		cancel()
	}
	for range streams {
		if err := <-results; status.Code(err) != codes.Canceled {
			t.Fatalf("Next() cancellation code = %v; error = %v", status.Code(err), err)
		}
	}
	for _, stream := range streams {
		if err := stream.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}
	if active.Load() != 0 {
		t.Fatalf("active targets after cancellation = %d", active.Load())
	}
}

func TestCoordinatorCloseCancelsRunningTargets(t *testing.T) {
	started := make(chan struct{}, 1)
	var active atomic.Int32
	executor := coordinatorTestExecutor(t, Config{
		AllowUnboundedFanout: true,
		PerDeviceTimeout:     5 * time.Second,
		TotalTimeout:         5 * time.Second,
	}, func(ctx context.Context, _ device.Device, _ redfish.Options) (sourceClient, error) {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	stream, err := executor.Query(
		context.Background(),
		querySnapshot(t, standaloneQueryDevice(t, "dc-a", "bmc-01")),
		queryRequest("SYSTEMS", 1),
	)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	nextDone := make(chan error, 1)
	go func() {
		_, err := stream.Next(context.Background())
		nextDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("target did not start")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- stream.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close() did not release the running target")
	}
	select {
	case err := <-nextDone:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Next() after Close error = %v, want EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Next() remained blocked after Close")
	}
	if active.Load() != 0 {
		t.Fatalf("active targets after Close = %d", active.Load())
	}
}

func TestCoordinatorCapturesFixedTargetsAndSortsPartialWarnings(t *testing.T) {
	var calls atomic.Int32
	executor := coordinatorTestExecutor(t, Config{
		AllowPartialResults:  true,
		AllowUnboundedFanout: true,
		MaxDevicesPerQuery:   2,
		MaxConcurrentDevices: 2,
	}, func(_ context.Context, _ device.Device, _ redfish.Options) (sourceClient, error) {
		calls.Add(1)
		return nil, status.Error(codes.Unavailable, "synthetic target unavailable")
	})
	configured := []device.Device{
		standaloneQueryDevice(t, "dc/a", "bmc-02"),
		standaloneQueryDevice(t, "dc/a", "bmc-01"),
	}
	snapshot := querySnapshot(t, configured...)
	request := queryRequest("SYSTEMS", 1)
	request.AcceptOutcome = true
	request.AllowPartialResults = true
	stream, err := executor.Query(context.Background(), snapshot, request)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	configured[0] = standaloneQueryDevice(t, "changed", "changed")
	detached := snapshot.Devices()
	detached[0] = standaloneQueryDevice(t, "also-changed", "also-changed")
	consumeStream(t, stream)
	outcome := streamOutcome(t, stream)
	if outcome.GetCompletion() != providerv1.QueryCompletion_QUERY_COMPLETION_PARTIAL {
		t.Fatalf("completion = %v", outcome.GetCompletion())
	}
	gotTargets := make([]string, 0, len(outcome.GetWarnings()))
	for _, warning := range outcome.GetWarnings() {
		if warning.GetCode() != "REDFISH_TARGET_UNAVAILABLE" || !warning.GetRetryable() ||
			warning.GetRole() != providerv1.QueryDiagnosticRole_QUERY_DIAGNOSTIC_ROLE_PARTIAL_RESULT_CAUSE {
			t.Fatalf("warning = %v", warning)
		}
		gotTargets = append(gotTargets, warning.GetTarget())
	}
	wantTargets := []string{
		"namespace=dc%2Fa&device_id=bmc-01",
		"namespace=dc%2Fa&device_id=bmc-02",
	}
	if !reflect.DeepEqual(gotTargets, wantTargets) {
		t.Fatalf("warning targets = %v, want %v", gotTargets, wantTargets)
	}
	if calls.Load() != 2 {
		t.Fatalf("client factory calls = %d, want 2", calls.Load())
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestCoordinatorEnforcesPerDeviceAndTotalTimeouts(t *testing.T) {
	blockingFactory := func(ctx context.Context, _ device.Device, _ redfish.Options) (sourceClient, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Run("per-device partial", func(t *testing.T) {
		executor := coordinatorTestExecutor(t, Config{
			AllowPartialResults:  true,
			AllowUnboundedFanout: true,
			PerDeviceTimeout:     40 * time.Millisecond,
			TotalTimeout:         2 * time.Second,
		}, blockingFactory)
		request := queryRequest("SYSTEMS", 1, equality("device_id", "bmc-01"))
		request.AcceptOutcome = true
		request.AllowPartialResults = true
		stream, err := executor.Query(
			context.Background(),
			querySnapshot(t, standaloneQueryDevice(t, "dc-a", "bmc-01")),
			request,
		)
		if err != nil {
			t.Fatalf("Query() error = %v", err)
		}
		consumeStream(t, stream)
		outcome := streamOutcome(t, stream)
		if outcome.GetCompletion() != providerv1.QueryCompletion_QUERY_COMPLETION_PARTIAL ||
			len(outcome.GetWarnings()) != 1 ||
			outcome.GetWarnings()[0].GetCode() != "REDFISH_TARGET_TIMEOUT" {
			t.Fatalf("outcome = %v", outcome)
		}
		_ = stream.Close()
	})

	t.Run("per-device strict", func(t *testing.T) {
		executor := coordinatorTestExecutor(t, Config{
			AllowUnboundedFanout: true,
			PerDeviceTimeout:     40 * time.Millisecond,
			TotalTimeout:         2 * time.Second,
		}, blockingFactory)
		stream, err := executor.Query(
			context.Background(),
			querySnapshot(t, standaloneQueryDevice(t, "dc-a", "bmc-01")),
			queryRequest("SYSTEMS", 1, equality("device_id", "bmc-01")),
		)
		if err != nil {
			t.Fatalf("Query() error = %v", err)
		}
		if _, err := stream.Next(context.Background()); status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("Next() code = %v; error = %v", status.Code(err), err)
		}
		_ = stream.Close()
	})

	t.Run("total partial", func(t *testing.T) {
		executor := coordinatorTestExecutor(t, Config{
			AllowPartialResults:  true,
			AllowUnboundedFanout: true,
			MaxDevicesPerQuery:   2,
			MaxConcurrentDevices: 1,
			PerDeviceTimeout:     2 * time.Second,
			TotalTimeout:         40 * time.Millisecond,
		}, blockingFactory)
		request := queryRequest("SYSTEMS", 1)
		request.AcceptOutcome = true
		request.AllowPartialResults = true
		stream, err := executor.Query(
			context.Background(),
			querySnapshot(t,
				standaloneQueryDevice(t, "dc-a", "bmc-01"),
				standaloneQueryDevice(t, "dc-a", "bmc-02"),
			),
			request,
		)
		if err != nil {
			t.Fatalf("Query() error = %v", err)
		}
		consumeStream(t, stream)
		outcome := streamOutcome(t, stream)
		if outcome.GetCompletion() != providerv1.QueryCompletion_QUERY_COMPLETION_PARTIAL ||
			len(outcome.GetWarnings()) != 2 {
			t.Fatalf("outcome = %v", outcome)
		}
		for _, warning := range outcome.GetWarnings() {
			if warning.GetCode() != "REDFISH_QUERY_TIMEOUT" || !warning.GetRetryable() {
				t.Fatalf("warning = %v", warning)
			}
		}
		_ = stream.Close()
	})
}

func TestExecutorRejectsPartialResultsUnlessNegotiated(t *testing.T) {
	target := standaloneQueryDevice(t, "dc-a", "bmc-01")
	request := queryRequest("SYSTEMS", 1, equality("device_id", "bmc-01"))
	request.AllowPartialResults = true

	disabled := coordinatorTestExecutor(t, Config{}, func(context.Context, device.Device, redfish.Options) (sourceClient, error) {
		t.Fatal("client factory must not be called")
		return nil, nil
	})
	request.AcceptOutcome = true
	if _, err := disabled.Query(context.Background(), querySnapshot(t, target), request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("disabled partial code = %v; error = %v", status.Code(err), err)
	}

	enabled := coordinatorTestExecutor(t, Config{AllowPartialResults: true}, func(context.Context, device.Device, redfish.Options) (sourceClient, error) {
		t.Fatal("client factory must not be called")
		return nil, nil
	})
	request.AcceptOutcome = false
	if _, err := enabled.Query(context.Background(), querySnapshot(t, target), request); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unaccepted partial code = %v; error = %v", status.Code(err), err)
	}
}

func TestCoordinatorReportsRowLimitsWithoutDiscardingValidRows(t *testing.T) {
	newServer := func(t *testing.T, id string, members string) *testredfish.Server {
		t.Helper()
		return testredfish.NewServer(t, testredfish.Options{
			ID:             id,
			Username:       "operator",
			Password:       "password",
			Authentication: testredfish.AuthenticationBasic,
			Resources: map[string]json.RawMessage{
				"/redfish/v1/Systems": json.RawMessage(`{"Members":` + members + `}`),
			},
		})
	}
	t.Run("per device", func(t *testing.T) {
		server := newServer(t, "per-device-limit", `[
			{"@odata.id":"/redfish/v1/Systems/1","Id":"one","Name":"One"},
			{"@odata.id":"/redfish/v1/Systems/2","Id":"two","Name":"Two"}
		]`)
		executor := queryTestExecutor(t, Config{
			AllowPartialResults: true,
			MaxRowsPerDevice:    1,
			MaxRowsPerQuery:     10,
			BatchRows:           8,
		})
		request := queryRequest("SYSTEMS", 8, equality("device_id", "bmc-01"))
		request.AcceptOutcome = true
		request.AllowPartialResults = true
		stream, err := executor.Query(
			context.Background(),
			querySnapshot(t, queryTestDevice(t, "dc-a", "bmc-01", server)),
			request,
		)
		if err != nil {
			t.Fatalf("Query() error = %v", err)
		}
		tuples := consumeStream(t, stream)
		outcome := streamOutcome(t, stream)
		if len(tuples) != 1 || outcome.GetCompletion() != providerv1.QueryCompletion_QUERY_COMPLETION_PARTIAL ||
			len(outcome.GetWarnings()) != 1 ||
			outcome.GetWarnings()[0].GetCode() != "REDFISH_RESULT_LIMIT_EXCEEDED" {
			t.Fatalf("limited result = tuples %v, outcome %v", tuples, outcome)
		}
		_ = stream.Close()
	})

	t.Run("whole query", func(t *testing.T) {
		first := newServer(t, "query-limit-first", `[
			{"@odata.id":"/redfish/v1/Systems/1","Id":"one","Name":"One"}
		]`)
		second := newServer(t, "query-limit-second", `[
			{"@odata.id":"/redfish/v1/Systems/1","Id":"two","Name":"Two"}
		]`)
		executor := queryTestExecutor(t, Config{
			AllowPartialResults:  true,
			AllowUnboundedFanout: true,
			MaxDevicesPerQuery:   2,
			MaxConcurrentDevices: 1,
			MaxRowsPerDevice:     10,
			MaxRowsPerQuery:      1,
		})
		request := queryRequest("SYSTEMS", 1)
		request.AcceptOutcome = true
		request.AllowPartialResults = true
		stream, err := executor.Query(
			context.Background(),
			querySnapshot(t,
				queryTestDevice(t, "dc-a", "bmc-01", first),
				queryTestDevice(t, "dc-a", "bmc-02", second),
			),
			request,
		)
		if err != nil {
			t.Fatalf("Query() error = %v", err)
		}
		tuples := consumeStream(t, stream)
		outcome := streamOutcome(t, stream)
		if len(tuples) != 1 || outcome.GetCompletion() != providerv1.QueryCompletion_QUERY_COMPLETION_PARTIAL ||
			len(outcome.GetWarnings()) != 1 ||
			outcome.GetWarnings()[0].GetTarget() != "namespace=dc-a&device_id=bmc-02" ||
			outcome.GetWarnings()[0].GetCode() != "REDFISH_RESULT_LIMIT_EXCEEDED" {
			t.Fatalf("limited result = tuples %v, outcome %v", tuples, outcome)
		}
		_ = stream.Close()
	})
}

func coordinatorTestExecutor(t *testing.T, config Config, factory clientFactory) *Executor {
	t.Helper()
	executor, err := newExecutor(config, factory)
	if err != nil {
		t.Fatalf("newExecutor() error = %v", err)
	}
	return executor
}

func consumeStream(t *testing.T, stream providersdk.ResultStream) []*providerv1.Tuple {
	t.Helper()
	var tuples []*providerv1.Tuple
	for {
		batch, err := stream.Next(context.Background())
		if errors.Is(err, io.EOF) {
			return tuples
		}
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		tuples = append(tuples, batch.GetTuples()...)
	}
}

func streamOutcome(t *testing.T, stream providersdk.ResultStream) *providerv1.QueryOutcome {
	t.Helper()
	outcomeStream, ok := stream.(providersdk.QueryOutcomeStream)
	if !ok {
		t.Fatalf("stream %T has no outcome", stream)
	}
	outcome := outcomeStream.Outcome()
	if outcome == nil {
		t.Fatal("Outcome() = nil")
	}
	return outcome
}

func updateMaximum(maximum *atomic.Int32, candidate int32) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}
