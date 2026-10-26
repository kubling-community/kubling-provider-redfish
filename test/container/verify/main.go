// Command verify validates the built provider image against two synthetic BMCs.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	defaultProviderAddress = "provider:50051"
	providerNamespace      = "redfish"
	startupTimeout         = 30 * time.Second
)

func main() {
	if err := run(); err != nil {
		slog.Error("container end-to-end verification failed", "error", err)
		os.Exit(1)
	}
	slog.Info("container end-to-end verification passed", "devices", 2)
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	if err := waitForBMCs(ctx, strings.Split(os.Getenv("KUBLING_REDFISH_E2E_BMCS"), ",")); err != nil {
		return err
	}
	address := strings.TrimSpace(os.Getenv("KUBLING_REDFISH_E2E_ADDRESS"))
	if address == "" {
		address = defaultProviderAddress
	}
	connection, err := grpc.NewClient(
		"dns:///"+address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("create provider client: %w", err)
	}
	defer connection.Close()
	client := providerv1.NewProviderServiceClient(connection)
	if err := waitForProvider(ctx, client); err != nil {
		return err
	}

	schema, err := client.GetSchema(ctx, &providerv1.GetSchemaRequest{})
	if err != nil {
		return fmt.Errorf("get schema: %w", err)
	}
	if len(schema.GetMetadata().GetTables()) != 22 {
		return fmt.Errorf("schema contains %d tables, want 22", len(schema.GetMetadata().GetTables()))
	}

	opened, err := client.OpenConnection(ctx, &providerv1.OpenConnectionRequest{})
	if err != nil {
		return fmt.Errorf("open connection: %w", err)
	}
	connectionID := opened.GetConnectionId()
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer closeCancel()
		_, _ = client.CloseConnection(closeCtx, &providerv1.CloseConnectionRequest{
			ConnectionId: connectionID,
		})
	}()
	namespaceRows, namespaceOutcome, err := queryTable(ctx, client, connectionID, "NAMESPACES", nil)
	if err != nil {
		return fmt.Errorf("namespace registry query: %w", err)
	}
	if err := verifyComplete(namespaceOutcome); err != nil {
		return fmt.Errorf("namespace registry query: %w", err)
	}
	if len(namespaceRows) != 1 || stringValue(namespaceRows[0], "namespace") != "e2e" ||
		namespaceRows[0]["device_count"].GetLongValue() != 2 {
		return fmt.Errorf("namespace registry query returned unexpected rows: %v", namespaceRows)
	}

	rows, outcome, err := querySystems(ctx, client, connectionID, nil)
	if err != nil {
		return fmt.Errorf("fan-out query: %w", err)
	}
	if err := verifyFanout(rows, outcome); err != nil {
		return err
	}
	routedRows, routedOutcome, err := querySystems(
		ctx,
		client,
		connectionID,
		logicalAnd(equality("namespace", "e2e"), equality("device_id", "bmc-a")),
	)
	if err != nil {
		return fmt.Errorf("routed query: %w", err)
	}
	if err := verifyComplete(routedOutcome); err != nil {
		return fmt.Errorf("routed query: %w", err)
	}
	if len(routedRows) != 1 || stringValue(routedRows[0], "device_id") != "bmc-a" {
		return fmt.Errorf("routed query returned unexpected rows: %v", summarizeRows(routedRows))
	}
	return nil
}

func waitForBMCs(ctx context.Context, endpoints []string) error {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} // Synthetic endpoints only.
	client := &http.Client{Transport: transport, Timeout: time.Second}
	for _, endpoint := range endpoints {
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" {
			return errors.New("KUBLING_REDFISH_E2E_BMCS must list the synthetic BMC endpoints")
		}
		if err := waitUntil(ctx, func() error {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if err != nil {
				return err
			}
			response, err := client.Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("status %s", response.Status)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("wait for synthetic BMC %q: %w", endpoint, err)
		}
	}
	return nil
}

func waitForProvider(ctx context.Context, client providerv1.ProviderServiceClient) error {
	if err := waitUntil(ctx, func() error {
		callCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		health, err := client.Health(callCtx, &providerv1.HealthRequest{})
		if err != nil {
			return err
		}
		if !health.GetHealthy() {
			return errors.New("provider reported unhealthy")
		}
		return nil
	}); err != nil {
		return fmt.Errorf("wait for provider: %w", err)
	}
	return nil
}

func waitUntil(ctx context.Context, check func() error) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := check(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func querySystems(
	ctx context.Context,
	client providerv1.ProviderServiceClient,
	connectionID string,
	filter *providerv1.Expression,
) ([]map[string]*kublingv1.Value, *providerv1.QueryOutcome, error) {
	return queryTable(ctx, client, connectionID, "SYSTEMS", filter)
}

func queryTable(
	ctx context.Context,
	client providerv1.ProviderServiceClient,
	connectionID string,
	table string,
	filter *providerv1.Expression,
) ([]map[string]*kublingv1.Value, *providerv1.QueryOutcome, error) {
	batchSize := uint32(2)
	stream, err := client.Query(ctx, &providerv1.QueryRequest{
		ConnectionId: connectionID,
		Entity: &providerv1.EntityReference{
			Name:      table,
			Namespace: providerNamespace,
		},
		Filter:        filter,
		BatchSize:     &batchSize,
		AcceptOutcome: true,
	})
	if err != nil {
		return nil, nil, err
	}
	var rows []map[string]*kublingv1.Value
	var outcome *providerv1.QueryOutcome
	for {
		response, receiveErr := stream.Recv()
		if errors.Is(receiveErr, io.EOF) {
			return rows, outcome, nil
		}
		if receiveErr != nil {
			return rows, outcome, receiveErr
		}
		if response.GetOutcome() != nil {
			outcome = response.GetOutcome()
			continue
		}
		batch := response.GetBatch()
		for _, tuple := range batch.GetTuples() {
			if len(tuple.GetValues()) != len(batch.GetFields()) {
				return nil, nil, errors.New("provider returned an invalid tuple shape")
			}
			row := make(map[string]*kublingv1.Value, len(batch.GetFields()))
			for index, field := range batch.GetFields() {
				row[field.GetName()] = tuple.GetValues()[index]
			}
			rows = append(rows, row)
		}
	}
}

func verifyFanout(rows []map[string]*kublingv1.Value, outcome *providerv1.QueryOutcome) error {
	if err := verifyComplete(outcome); err != nil {
		return fmt.Errorf("fan-out query: %w", err)
	}
	if len(rows) != 2 {
		return fmt.Errorf("fan-out query returned %d rows, want 2: %v", len(rows), summarizeRows(rows))
	}
	identities := make([]string, 0, len(rows))
	for _, row := range rows {
		if stringValue(row, "namespace") != "e2e" || stringValue(row, "redfish_id") != "system-1" ||
			stringValue(row, "resource_uri") != "/redfish/v1/Systems/1" {
			return fmt.Errorf("fan-out query returned an unexpected row: %v", summarizeRows([]map[string]*kublingv1.Value{row}))
		}
		identities = append(
			identities,
			stringValue(row, "namespace")+"/"+stringValue(row, "device_id")+stringValue(row, "resource_uri"),
		)
	}
	sort.Strings(identities)
	want := []string{
		"e2e/bmc-a/redfish/v1/Systems/1",
		"e2e/bmc-b/redfish/v1/Systems/1",
	}
	if strings.Join(identities, "\n") != strings.Join(want, "\n") {
		return fmt.Errorf("fan-out identities = %v, want %v", identities, want)
	}
	return nil
}

func verifyComplete(outcome *providerv1.QueryOutcome) error {
	if outcome == nil {
		return errors.New("provider did not return a query outcome")
	}
	if outcome.GetCompletion() != providerv1.QueryCompletion_QUERY_COMPLETION_COMPLETE ||
		len(outcome.GetWarnings()) != 0 {
		return fmt.Errorf("unexpected query outcome: %v", outcome)
	}
	return nil
}

func stringValue(row map[string]*kublingv1.Value, field string) string {
	return row[field].GetStringValue()
}

func summarizeRows(rows []map[string]*kublingv1.Value) []string {
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		result = append(result, fmt.Sprintf(
			"%s/%s%s",
			stringValue(row, "namespace"),
			stringValue(row, "device_id"),
			stringValue(row, "resource_uri"),
		))
	}
	return result
}

func equality(field string, value string) *providerv1.Expression {
	return &providerv1.Expression{Kind: &providerv1.Expression_Comparison{Comparison: &providerv1.ComparisonExpression{
		Operator: providerv1.ComparisonOperator_COMPARISON_OPERATOR_EQUAL,
		Left: &providerv1.Expression{Kind: &providerv1.Expression_Field{Field: &providerv1.FieldReference{
			Name: field,
		}}},
		Right: &providerv1.Expression{Kind: &providerv1.Expression_Literal{Literal: &providerv1.Literal{
			Value: &kublingv1.Value{Kind: &kublingv1.Value_StringValue{StringValue: value}},
		}}},
	}}}
}

func logicalAnd(operands ...*providerv1.Expression) *providerv1.Expression {
	return &providerv1.Expression{Kind: &providerv1.Expression_Logical{Logical: &providerv1.LogicalExpression{
		Operator: providerv1.LogicalOperator_LOGICAL_OPERATOR_AND,
		Operands: operands,
	}}}
}
