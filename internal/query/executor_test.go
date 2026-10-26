package query

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	"github.com/kubling-community/kubling-provider-redfish/internal/redfish"
	"github.com/kubling-community/kubling-provider-redfish/internal/secret"
	"github.com/kubling-community/kubling-provider-redfish/internal/testredfish"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExecutorStreamsOpaquePaginationInlineAndLinkedMembersWithBackpressure(t *testing.T) {
	var pageCalls atomic.Int32
	var queryMu sync.Mutex
	var opaqueQuery string
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "pagination",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
		Resources: map[string]json.RawMessage{
			"/redfish/v1/Systems/2": json.RawMessage(`{
				"@odata.id":"/redfish/v1/Systems/2",
				"@odata.type":"#ComputerSystem.v1_29_0.ComputerSystem",
				"Id":"system-2",
				"Name":"Linked system"
			}`),
		},
		Routes: map[string]http.Handler{
			"/redfish/v1/Systems": http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				pageCalls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				if request.URL.RawQuery == "" {
					_, _ = io.WriteString(writer, `{
						"Members":[
							{"@odata.id":"/redfish/v1/Systems/1","Id":"system-1","Name":"Inline system"},
							{"@odata.id":"/redfish/v1/Systems/2"}
						],
						"Members@odata.nextLink":"/redfish/v1/Systems?$skiptoken=opaque%2Bcursor%26part"
					}`)
					return
				}
				queryMu.Lock()
				opaqueQuery = request.URL.RawQuery
				queryMu.Unlock()
				_, _ = io.WriteString(writer, `{
					"Members":[
						{"@odata.id":"/redfish/v1/Systems/1","Id":"system-1","Name":"Duplicate system"},
						{"@odata.id":"/redfish/v1/Systems/3","Id":"system-3","Name":"Third system"}
					]
				}`)
			}),
		},
	})

	executor := queryTestExecutor(t, Config{})
	stream, err := executor.Query(
		context.Background(),
		querySnapshot(t, queryTestDevice(t, "dc-a", "bmc-01", server)),
		queryRequest("SYSTEMS", 1, equality("namespace", "dc-a"), equality("device_id", "bmc-01")),
	)
	if err != nil {
		t.Fatalf("Executor.Query() error = %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if pageCalls.Load() != 0 {
		t.Fatalf("collection requests before Next = %d", pageCalls.Load())
	}

	var identifiers []string
	for index := 0; index < 3; index++ {
		batch, err := stream.Next(context.Background())
		if err != nil {
			t.Fatalf("Next(%d) error = %v", index, err)
		}
		if len(batch.GetTuples()) != 1 {
			t.Fatalf("Next(%d) tuple count = %d", index, len(batch.GetTuples()))
		}
		identifiers = append(identifiers, batch.GetTuples()[0].GetValues()[1].GetStringValue())
		switch index {
		case 0:
			if pageCalls.Load() != 1 || server.Count(http.MethodGet, "/redfish/v1/Systems/2") != 0 {
				t.Fatalf("first batch fetched ahead: pages=%d requests=%v", pageCalls.Load(), server.Requests())
			}
		case 1:
			if pageCalls.Load() != 1 || server.Count(http.MethodGet, "/redfish/v1/Systems/2") != 1 {
				t.Fatalf("second batch requests = %v", server.Requests())
			}
		case 2:
			if pageCalls.Load() != 2 {
				t.Fatalf("third batch page calls = %d", pageCalls.Load())
			}
		}
	}
	if _, err := stream.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Next(end) error = %v, want EOF", err)
	}
	if !reflect.DeepEqual(identifiers, []string{"system-1", "system-2", "system-3"}) {
		t.Fatalf("identifiers = %v", identifiers)
	}
	queryMu.Lock()
	gotOpaqueQuery := opaqueQuery
	queryMu.Unlock()
	if gotOpaqueQuery != "$skiptoken=opaque%2Bcursor%26part" {
		t.Fatalf("opaque query = %q", gotOpaqueQuery)
	}
}

func TestExecutorUsesDirectResourceURIWithoutReadingCollection(t *testing.T) {
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "direct",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
		Resources: map[string]json.RawMessage{
			"/redfish/v1/Systems/42": json.RawMessage(`{
				"@odata.id":"/redfish/v1/Systems/42",
				"Id":"system-42",
				"Name":"Direct system"
			}`),
		},
		Routes: map[string]http.Handler{
			"/redfish/v1/Systems": http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				t.Error("collection must not be read for direct resource_uri")
				writer.WriteHeader(http.StatusInternalServerError)
			}),
		},
	})
	executor := queryTestExecutor(t, Config{})
	stream, err := executor.Query(
		context.Background(),
		querySnapshot(t, queryTestDevice(t, "dc-a", "bmc-01", server)),
		queryRequest(
			"SYSTEMS",
			2,
			equality("namespace", "dc-a"),
			equality("device_id", "bmc-01"),
			equality("resource_uri", "/redfish/v1/Systems/42"),
		),
	)
	if err != nil {
		t.Fatalf("Executor.Query() error = %v", err)
	}
	defer stream.Close()
	batch, err := stream.Next(context.Background())
	if err != nil || len(batch.GetTuples()) != 1 ||
		batch.GetTuples()[0].GetValues()[1].GetStringValue() != "system-42" {
		t.Fatalf("Next() = (%v, %v)", batch, err)
	}
	if server.Count(http.MethodGet, "/redfish/v1/Systems") != 0 ||
		server.Count(http.MethodGet, "/redfish/v1/Systems/42") != 1 {
		t.Fatalf("requests = %v", server.Requests())
	}
}

func TestExecutorDetectsPaginationCyclesAndLimitsWithoutSilentTruncation(t *testing.T) {
	tests := map[string]struct {
		config     Config
		collection string
		batchSize  uint32
		firstOK    bool
		wantCode   codes.Code
	}{
		"cycle": {
			collection: `{"Members":[],"Members@odata.nextLink":"/redfish/v1/Systems"}`,
			batchSize:  1,
			wantCode:   codes.DataLoss,
		},
		"page limit": {
			config:     Config{MaxPagesPerCollection: 1},
			collection: `{"Members":[],"Members@odata.nextLink":"/redfish/v1/Systems?page=2"}`,
			batchSize:  1,
			wantCode:   codes.ResourceExhausted,
		},
		"row limit": {
			config: Config{MaxRowsPerDevice: 1, MaxRowsPerQuery: 1},
			collection: `{"Members":[
				{"@odata.id":"/redfish/v1/Systems/1","Id":"one"},
				{"@odata.id":"/redfish/v1/Systems/2","Id":"two"}
			]}`,
			batchSize: 1,
			firstOK:   true,
			wantCode:  codes.ResourceExhausted,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := testredfish.NewServer(t, testredfish.Options{
				ID:             "limit",
				Username:       "operator",
				Password:       "password",
				Authentication: testredfish.AuthenticationBasic,
				Routes: map[string]http.Handler{
					"/redfish/v1/Systems": http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
						writer.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(writer, test.collection)
					}),
				},
			})
			executor := queryTestExecutor(t, test.config)
			stream, err := executor.Query(
				context.Background(),
				querySnapshot(t, queryTestDevice(t, "dc-a", "bmc-01", server)),
				queryRequest("SYSTEMS", test.batchSize, equality("device_id", "bmc-01")),
			)
			if err != nil {
				t.Fatalf("Executor.Query() error = %v", err)
			}
			defer stream.Close()
			if test.firstOK {
				if _, err := stream.Next(context.Background()); err != nil {
					t.Fatalf("Next(first) error = %v", err)
				}
			}
			if _, err := stream.Next(context.Background()); status.Code(err) != test.wantCode {
				t.Fatalf("Next(limit) code = %v, want %v; error = %v", status.Code(err), test.wantCode, err)
			}
		})
	}
}

func TestExecutorRejectsUnboundedAndExcessiveFanoutBeforeNetworkAccess(t *testing.T) {
	first := standaloneQueryDevice(t, "dc-a", "bmc-01")
	second := standaloneQueryDevice(t, "dc-a", "bmc-02")
	snapshot := querySnapshot(t, first, second)
	var factoryCalls atomic.Int32
	executor, err := newExecutor(Config{MaxDevicesPerQuery: 1}, func(context.Context, device.Device, redfish.Options) (sourceClient, error) {
		factoryCalls.Add(1)
		return nil, errors.New("must not be called")
	})
	if err != nil {
		t.Fatalf("newExecutor() error = %v", err)
	}

	if _, err := executor.Query(context.Background(), snapshot, queryRequest("SYSTEMS", 1)); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unbounded Query() code = %v, error = %v", status.Code(err), err)
	}
	if _, err := executor.Query(
		context.Background(), snapshot, queryRequest("SYSTEMS", 1, equality("namespace", "dc-a")),
	); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("excessive Query() code = %v, error = %v", status.Code(err), err)
	}
	empty, err := executor.Query(
		context.Background(), snapshot, queryRequest("SYSTEMS", 1, equality("device_id", "missing")),
	)
	if err != nil {
		t.Fatalf("empty Query() error = %v", err)
	}
	if _, err := empty.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("empty.Next() error = %v", err)
	}
	_ = empty.Close()
	if factoryCalls.Load() != 0 {
		t.Fatalf("client factory calls = %d, want 0", factoryCalls.Load())
	}
}

func TestExecutorQueriesNamespaceRegistryWithoutRedfishAccess(t *testing.T) {
	snapshot := querySnapshot(
		t,
		standaloneQueryDevice(t, "dc-b", "bmc-03"),
		standaloneQueryDevice(t, "dc-a", "bmc-02"),
		standaloneQueryDevice(t, "dc-a", "bmc-01"),
	)
	var factoryCalls atomic.Int32
	executor, err := newExecutor(Config{BatchRows: 1}, func(context.Context, device.Device, redfish.Options) (sourceClient, error) {
		factoryCalls.Add(1)
		return nil, errors.New("registry query must not create a Redfish client")
	})
	if err != nil {
		t.Fatalf("newExecutor() error = %v", err)
	}
	batchSize := uint32(1)
	stream, err := executor.Query(context.Background(), snapshot, &providerv1.QueryRequest{
		Entity:        &providerv1.EntityReference{Name: "NAMESPACES", Namespace: catalog.Namespace},
		BatchSize:     &batchSize,
		AcceptOutcome: true,
	})
	if err != nil {
		t.Fatalf("Executor.Query(NAMESPACES) error = %v", err)
	}
	defer stream.Close()

	type namespaceResult struct {
		name  string
		count int64
	}
	var rows []namespaceResult
	for {
		batch, nextErr := stream.Next(context.Background())
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatalf("NAMESPACES.Next() error = %v", nextErr)
		}
		if len(batch.GetFields()) != 2 || len(batch.GetTuples()) != 1 ||
			len(batch.GetTuples()[0].GetValues()) != 2 {
			t.Fatalf("NAMESPACES batch = %v", batch)
		}
		values := make(map[string]*kublingv1.Value, 2)
		for index, field := range batch.GetFields() {
			values[field.GetName()] = batch.GetTuples()[0].GetValues()[index]
		}
		rows = append(rows, namespaceResult{
			name:  values["namespace"].GetStringValue(),
			count: values["device_count"].GetLongValue(),
		})
	}
	if !reflect.DeepEqual(rows, []namespaceResult{{name: "dc-a", count: 2}, {name: "dc-b", count: 1}}) {
		t.Fatalf("NAMESPACES rows = %v", rows)
	}
	outcomeStream, ok := stream.(providersdk.QueryOutcomeStream)
	if !ok {
		t.Fatalf("NAMESPACES stream %T does not expose an outcome", stream)
	}
	outcome := outcomeStream.Outcome()
	if outcome.GetCompletion() != providerv1.QueryCompletion_QUERY_COMPLETION_COMPLETE ||
		len(outcome.GetWarnings()) != 0 {
		t.Fatalf("NAMESPACES outcome = %v", outcome)
	}

	filtered, err := executor.Query(context.Background(), snapshot, &providerv1.QueryRequest{
		Entity: &providerv1.EntityReference{Name: "NAMESPACES", Namespace: catalog.Namespace},
		Filter: equality("namespace", "dc-b"),
	})
	if err != nil {
		t.Fatalf("filtered NAMESPACES query error = %v", err)
	}
	defer filtered.Close()
	batch, err := filtered.Next(context.Background())
	if err != nil || len(batch.GetTuples()) != 1 ||
		batch.GetTuples()[0].GetValues()[0].GetStringValue() != "dc-b" ||
		batch.GetTuples()[0].GetValues()[1].GetLongValue() != 1 {
		t.Fatalf("filtered NAMESPACES batch = (%v, %v)", batch, err)
	}
	if factoryCalls.Load() != 0 {
		t.Fatalf("Redfish client factory calls = %d, want 0", factoryCalls.Load())
	}
}

func queryTestExecutor(t *testing.T, config Config) *Executor {
	t.Helper()
	config.PerDeviceTimeout = 2 * time.Second
	config.TotalTimeout = 5 * time.Second
	config.Retry = redfish.RetryPolicy{MaxAttempts: 1, MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	executor, err := NewExecutor(config)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	return executor
}

func queryRequest(table string, batchSize uint32, filters ...*providerv1.Expression) *providerv1.QueryRequest {
	request := &providerv1.QueryRequest{
		Entity: &providerv1.EntityReference{Name: table, Namespace: catalog.Namespace},
		Projections: []*providerv1.Projection{
			fieldProjection("resource_uri", ""),
			fieldProjection("redfish_id", ""),
		},
	}
	if batchSize != 0 {
		request.BatchSize = &batchSize
	}
	switch len(filters) {
	case 0:
	case 1:
		request.Filter = filters[0]
	default:
		request.Filter = and(filters...)
	}
	return request
}

func queryTestDevice(t *testing.T, namespace string, id string, server *testredfish.Server) device.Device {
	t.Helper()
	return newQueryDevice(t, namespace, id, server.Endpoint(), server.CAFile())
}

func standaloneQueryDevice(t *testing.T, namespace string, id string) device.Device {
	t.Helper()
	return newQueryDevice(t, namespace, id, "https://"+id+".example.invalid", "")
}

func newQueryDevice(t *testing.T, namespace string, id string, endpoint string, caFile string) device.Device {
	t.Helper()
	username, err := secret.NewValue("operator")
	if err != nil {
		t.Fatalf("secret.NewValue(username) error = %v", err)
	}
	password, err := secret.NewValue("password")
	if err != nil {
		t.Fatalf("secret.NewValue(password) error = %v", err)
	}
	configured, err := device.New(device.Definition{
		Namespace:      namespace,
		ID:             id,
		Endpoint:       endpoint,
		CredentialName: "test",
		Credential:     device.NewCredential(username, password),
		AuthMode:       device.AuthModeBasic,
		TLS:            device.NewTLSConfig(caFile, "", false),
	})
	if err != nil {
		t.Fatalf("device.New() error = %v", err)
	}
	return configured
}

func querySnapshot(t *testing.T, devices ...device.Device) device.Snapshot {
	t.Helper()
	registry, err := device.NewRegistry(devices)
	if err != nil {
		t.Fatalf("device.NewRegistry() error = %v", err)
	}
	return registry.Snapshot()
}
