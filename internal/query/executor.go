package query

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"

	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	"github.com/kubling-community/kubling-provider-redfish/internal/redfish"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type clientFactory func(context.Context, device.Device, redfish.Options) (sourceClient, error)

// Executor plans, routes and coordinates bounded queries across the configured
// Redfish fleet.
type Executor struct {
	config        Config
	newClient     clientFactory
	slots         chan struct{}
	querySequence atomic.Uint64
}

// NewExecutor validates query limits and creates the fleet executor.
func NewExecutor(config Config) (*Executor, error) {
	return newExecutor(config, func(
		ctx context.Context,
		target device.Device,
		options redfish.Options,
	) (sourceClient, error) {
		return redfish.NewClient(ctx, target, options)
	})
}

func newExecutor(config Config, factory clientFactory) (*Executor, error) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if factory == nil {
		return nil, errors.New("Redfish query client factory is required")
	}
	return &Executor{
		config:    normalized,
		newClient: factory,
		slots:     make(chan struct{}, normalized.MaxConcurrentDevices),
	}, nil
}

// Query plans a request, captures its fixed target set and returns a lazy,
// backpressured result stream.
func (e *Executor) Query(
	ctx context.Context,
	devices device.Snapshot,
	request *providerv1.QueryRequest,
) (providersdk.ResultStream, error) {
	if err := contextStatus(ctx); err != nil {
		return nil, err
	}
	plan, err := buildPlan(request)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "plan Redfish query: %v", err)
	}
	if request.GetAllowPartialResults() && !request.GetAcceptOutcome() {
		return nil, status.Error(codes.InvalidArgument, "partial Redfish results require outcome acceptance")
	}
	if request.GetAllowPartialResults() && !e.config.AllowPartialResults {
		return nil, status.Error(codes.FailedPrecondition, "partial Redfish results are disabled")
	}
	if plan.table.Discovery.Kind == catalog.DiscoveryRegistry {
		return e.queryRegistry(devices, request, plan)
	}
	if !plan.route.impossible && plan.route.unbounded() && !e.config.AllowUnboundedFanout {
		return nil, status.Error(
			codes.FailedPrecondition,
			"unbounded Redfish access is disabled; constrain namespace or device_id",
		)
	}

	var targets []device.Device
	if !plan.route.impossible {
		targets = selectDevices(devices.Devices(), plan.route)
	}
	if len(targets) > e.config.MaxDevicesPerQuery {
		return nil, status.Errorf(
			codes.ResourceExhausted,
			"Redfish query selects %d devices, exceeding the configured maximum of %d",
			len(targets),
			e.config.MaxDevicesPerQuery,
		)
	}
	queryID := "redfish-" + strconv.FormatUint(e.querySequence.Add(1), 36)
	return newCoordinatorStream(ctx, request, plan, targets, e, queryID), nil
}

func selectDevices(devices []device.Device, route accessRoute) []device.Device {
	result := make([]device.Device, 0, len(devices))
	for _, candidate := range devices {
		if route.namespace != nil && candidate.Namespace() != *route.namespace {
			continue
		}
		if route.deviceID != nil && candidate.ID() != *route.deviceID {
			continue
		}
		result = append(result, candidate)
	}
	return result
}

func (e *Executor) acquireSlot(ctx context.Context) bool {
	select {
	case e.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *Executor) releaseSlot() {
	<-e.slots
}
