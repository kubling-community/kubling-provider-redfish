package query

import (
	"fmt"
	"sort"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type namespaceRow struct {
	namespace   string
	deviceCount int64
}

func (e *Executor) queryRegistry(
	devices device.Snapshot,
	request *providerv1.QueryRequest,
	plan queryPlan,
) (providersdk.ResultStream, error) {
	rows := namespaceRows(devices, plan.route)
	tuples := make([]*providerv1.Tuple, 0, len(rows))
	for _, row := range rows {
		tuple, err := projectNamespaceRow(plan, row)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "project Redfish namespace registry: %v", err)
		}
		tuples = append(tuples, tuple)
	}
	var requestedBatchSize *uint32
	if request.BatchSize != nil {
		value := request.GetBatchSize()
		requestedBatchSize = &value
	}
	return newStaticStream(plan.fields(), tuples, e.config.batchSize(requestedBatchSize)), nil
}

func namespaceRows(snapshot device.Snapshot, route accessRoute) []namespaceRow {
	if route.impossible {
		return nil
	}
	counts := make(map[string]int64)
	for _, configuredDevice := range snapshot.Devices() {
		namespace := configuredDevice.Namespace()
		if route.namespace != nil && namespace != *route.namespace {
			continue
		}
		counts[namespace]++
	}
	namespaces := make([]string, 0, len(counts))
	for namespace := range counts {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	rows := make([]namespaceRow, 0, len(namespaces))
	for _, namespace := range namespaces {
		rows = append(rows, namespaceRow{namespace: namespace, deviceCount: counts[namespace]})
	}
	return rows
}

func projectNamespaceRow(plan queryPlan, row namespaceRow) (*providerv1.Tuple, error) {
	values := make([]*kublingv1.Value, 0, len(plan.projections))
	for _, projection := range plan.projections {
		switch projection.column.Source {
		case catalog.ColumnSourceNamespace:
			values = append(values, stringValue(row.namespace))
		case catalog.ColumnSourceDeviceCount:
			values = append(values, &kublingv1.Value{Kind: &kublingv1.Value_LongValue{
				LongValue: row.deviceCount,
			}})
		default:
			return nil, fmt.Errorf(
				"column %q has unsupported registry source %q",
				projection.column.Name,
				projection.column.Source,
			)
		}
	}
	return &providerv1.Tuple{Values: values}, nil
}
