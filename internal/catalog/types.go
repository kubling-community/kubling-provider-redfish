// Package catalog exposes the generated, provider-owned Redfish relational
// catalog without coupling query execution to DMTF JSON Schema internals.
package catalog

import kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"

const IdentifierColumn = "identifier"

// ColumnSource identifies where query execution obtains a column value.
type ColumnSource string

const (
	ColumnSourcePayload     ColumnSource = "payload"
	ColumnSourceNamespace   ColumnSource = "namespace"
	ColumnSourceDeviceID    ColumnSource = "device_id"
	ColumnSourceResourceURI ColumnSource = "resource_uri"
	ColumnSourceObservedAt  ColumnSource = "observed_at"
	ColumnSourceContext     ColumnSource = "context"
	ColumnSourceVariant     ColumnSource = "variant"
	ColumnSourceDeviceCount ColumnSource = "device_count"
)

// DiscoveryKind identifies how a table is reached from the ServiceRoot.
type DiscoveryKind string

const (
	DiscoverySingleton  DiscoveryKind = "singleton"
	DiscoveryCollection DiscoveryKind = "collection"
	DiscoveryGraph      DiscoveryKind = "graph"
	DiscoveryRegistry   DiscoveryKind = "registry"
)

// TraversalMode controls how branches are evaluated for each route anchor.
type TraversalMode string

const (
	TraversalUnion    TraversalMode = "union"
	TraversalFallback TraversalMode = "fallback"
)

// TraversalKind describes one generic Redfish graph operation.
type TraversalKind string

const (
	TraversalSingleton       TraversalKind = "singleton"
	TraversalCollection      TraversalKind = "collection"
	TraversalCollectionLinks TraversalKind = "collection_links"
	TraversalLinked          TraversalKind = "linked"
	TraversalLinks           TraversalKind = "links"
	TraversalEmbedded        TraversalKind = "embedded"
)

// Predicate retains rows whose string property equals the generated value.
type Predicate struct {
	JSONPointer string
	Equals      string
}

// TraversalStep is a generated graph edge. Schema identity is retained for
// inspection; query execution uses only the generic operation and pointer.
type TraversalStep struct {
	Kind             TraversalKind
	JSONPointer      string
	SchemaFile       string
	SchemaDefinition string
	Capture          string
	Predicate        *Predicate
}

// TraversalBranch is one terminal alternative below a route anchor.
type TraversalBranch struct {
	ID      string
	Variant string
	Steps   []TraversalStep
}

// TraversalRoute enumerates anchors and then unions or falls back through its
// generated branches independently for every anchor.
type TraversalRoute struct {
	Prefix   []TraversalStep
	Mode     TraversalMode
	Branches []TraversalBranch
}

// Discovery describes the provider traversal required to find table rows.
type Discovery struct {
	Kind        DiscoveryKind
	URI         string
	JSONPointer string
	Routes      []TraversalRoute
}

// ColumnMapping selects a payload property for one generated traversal
// branch. Null mappings represent a property that does not exist in that
// Redfish model generation.
type ColumnMapping struct {
	BranchID    string
	JSONPointer string
	Null        bool
	Predicate   *Predicate
}

// Column describes one provider-returned column. The generated identifier
// column exists only in Kubling metadata and is not included here.
type Column struct {
	Name           string
	Source         ColumnSource
	JSONPointer    string
	ContextKey     string
	Type           kublingv1.ValueType
	NativeType     string
	Nullable       bool
	EqualitySearch bool
	Annotation     string
	Mappings       []ColumnMapping
}

// Table describes one generated Redfish table and its source traversal.
type Table struct {
	Name             string
	Annotation       string
	SchemaFile       string
	SchemaDefinition string
	Discovery        Discovery
	Columns          []Column
	StableKeyColumns []string
	RoutingColumns   []string
}

// MetadataOptions controls physical access requirements advertised to Kubling.
type MetadataOptions struct {
	AllowUnboundedFanout bool
}
