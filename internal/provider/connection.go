package provider

import (
	"context"
	"fmt"
	"sync"

	"github.com/kubling-community/kubling-provider-redfish/internal/device"
	redfishquery "github.com/kubling-community/kubling-provider-redfish/internal/query"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Connection is a logical connection to the Redfish provider.
type Connection struct {
	mu            sync.RWMutex
	closed        bool
	devices       device.Snapshot
	queryExecutor *redfishquery.Executor
}

// String formats connection cardinality without device credentials.
func (c *Connection) String() string {
	if c == nil {
		return "Redfish connection (nil)"
	}
	return fmt.Sprintf("Redfish connection (%d devices)", c.devices.Len())
}

// GoString formats connection cardinality without device credentials.
func (c *Connection) GoString() string {
	return c.String()
}

// Close releases this logical connection.
func (c *Connection) Close(context.Context) error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

// Begin reports that the provider has no native local transactions.
func (c *Connection) Begin(ctx context.Context) error {
	if err := c.checkOpen(ctx); err != nil {
		return err
	}
	return transactionsUnsupportedError()
}

// Commit reports that the provider has no native local transactions.
func (c *Connection) Commit(ctx context.Context) error {
	if err := c.checkOpen(ctx); err != nil {
		return err
	}
	return transactionsUnsupportedError()
}

// Rollback reports that the provider has no native local transactions.
func (c *Connection) Rollback(ctx context.Context) error {
	if err := c.checkOpen(ctx); err != nil {
		return err
	}
	return transactionsUnsupportedError()
}

// InTransaction reports that no native local transaction can be active.
func (c *Connection) InTransaction(ctx context.Context) (bool, error) {
	if err := c.checkOpen(ctx); err != nil {
		return false, err
	}
	return false, transactionsUnsupportedError()
}

// Query executes the generated catalog against the selected Redfish device.
func (c *Connection) Query(
	ctx context.Context,
	request *providerv1.QueryRequest,
) (providersdk.ResultStream, error) {
	if err := c.checkOpen(ctx); err != nil {
		return nil, err
	}
	if c.queryExecutor == nil {
		return nil, status.Error(codes.Internal, "Redfish query executor is unavailable")
	}
	return c.queryExecutor.Query(ctx, c.devices, request)
}

// Insert reports that Redfish inventory is read-only.
func (c *Connection) Insert(
	ctx context.Context,
	_ *providerv1.InsertRequest,
) (*providerv1.InsertResponse, error) {
	if err := c.checkOpen(ctx); err != nil {
		return nil, err
	}
	return nil, mutationsUnsupportedError()
}

// Update reports that Redfish inventory is read-only.
func (c *Connection) Update(
	ctx context.Context,
	_ *providerv1.UpdateRequest,
) (*providerv1.UpdateResponse, error) {
	if err := c.checkOpen(ctx); err != nil {
		return nil, err
	}
	return nil, mutationsUnsupportedError()
}

// Delete reports that Redfish inventory is read-only.
func (c *Connection) Delete(
	ctx context.Context,
	_ *providerv1.DeleteRequest,
) (*providerv1.DeleteResponse, error) {
	if err := c.checkOpen(ctx); err != nil {
		return nil, err
	}
	return nil, mutationsUnsupportedError()
}

func (c *Connection) checkOpen(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return status.Error(codes.FailedPrecondition, "connection is closed")
	}
	return nil
}

func transactionsUnsupportedError() error {
	return status.Error(
		codes.Unimplemented,
		"native transactions are not supported; use Kubling soft transactions",
	)
}

func mutationsUnsupportedError() error {
	return status.Error(codes.Unimplemented, "Redfish provider is read-only")
}

var _ providersdk.Connection = (*Connection)(nil)
