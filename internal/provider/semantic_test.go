package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	"github.com/kubling-community/kubling-provider-redfish/internal/semanticmodel"
	"github.com/kubling-community/kubling-provider-redfish/internal/testredfish"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	providersdk "github.com/kubling-community/kubling-providers/sdk-go/provider"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

const redfishSemanticFragmentDigest = "sha256:f8a75751c6c0be0a55491a5f3c8b3fa26160f94d0afdd5cc8810b043d535c44f"

type semanticTestDocument struct {
	Format        string `yaml:"format"`
	SchemaVersion int    `yaml:"schemaVersion"`
	Kind          string `yaml:"kind"`
	Metadata      struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
		Version   string `yaml:"version"`
	} `yaml:"metadata"`
	Spec struct {
		Entities      []semanticTestEntity       `yaml:"entities"`
		Relationships []semanticTestRelationship `yaml:"relationships"`
	} `yaml:"spec"`
}

type semanticTestEntity struct {
	ID      string `yaml:"id"`
	Binding struct {
		Relation string `yaml:"relation"`
	} `yaml:"binding"`
	Properties []semanticTestProperty `yaml:"properties"`
	Identities []struct {
		ID         string   `yaml:"id"`
		Properties []string `yaml:"properties"`
	} `yaml:"identities"`
}

type semanticTestProperty struct {
	ID      string `yaml:"id"`
	Type    string `yaml:"type"`
	Binding struct {
		Field string `yaml:"field"`
	} `yaml:"binding"`
}

type semanticTestRelationship struct {
	ID          string `yaml:"id"`
	From        string `yaml:"from"`
	To          string `yaml:"to"`
	Cardinality string `yaml:"cardinality"`
	Joins       []struct {
		Left  string `yaml:"left"`
		Right string `yaml:"right"`
	} `yaml:"joins"`
}

func TestRedfishSemanticFragmentIsGeneratedDeterministicAndConnectionAgnostic(t *testing.T) {
	generated, err := semanticmodel.Generate()
	if err != nil {
		t.Fatalf("semanticmodel.Generate() error = %v", err)
	}
	secondGeneration, err := semanticmodel.Generate()
	if err != nil {
		t.Fatalf("second semanticmodel.Generate() error = %v", err)
	}
	if !bytes.Equal(generated, secondGeneration) {
		t.Fatal("successive semantic generations are not byte-identical")
	}
	if !bytes.Equal(generated, redfishSemanticFragmentDocument) {
		t.Fatal("embedded Redfish semantic fragment is stale; run make semantic")
	}

	source := testredfish.NewServer(t, testredfish.Options{
		ID:             "semantic",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
	})
	implementation := queryTestProvider(t, source)
	server := providersdk.NewServer(implementation)
	first, err := server.GetSemanticFragment(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetSemanticFragment() error = %v", err)
	}
	second, err := server.GetSemanticFragment(context.Background(), nil)
	if err != nil {
		t.Fatalf("second GetSemanticFragment() error = %v", err)
	}
	if len(source.Requests()) != 0 {
		t.Fatalf("semantic retrieval contacted the BMC: %v", source.Requests())
	}

	firstFragment := first.GetFragment()
	secondFragment := second.GetFragment()
	if firstFragment == nil || secondFragment == nil {
		t.Fatalf("semantic fragments = %v, %v", firstFragment, secondFragment)
	}
	if !bytes.Equal(firstFragment.GetDocument(), secondFragment.GetDocument()) {
		t.Fatal("semantic fragment bytes are not deterministic")
	}
	if firstFragment.GetMediaType() != providersdk.SemanticFragmentMediaTypeYAML ||
		firstFragment.GetVersion() != redfishSemanticFragmentVersion {
		t.Fatalf("semantic fragment envelope = %v", firstFragment)
	}
	if len(firstFragment.GetDocument()) > providersdk.MaxSemanticFragmentDocumentSize {
		t.Fatalf("semantic fragment size = %d", len(firstFragment.GetDocument()))
	}
	wantDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(firstFragment.GetDocument()))
	if firstFragment.GetDigest() != wantDigest || wantDigest != redfishSemanticFragmentDigest {
		t.Fatalf(
			"digest = %q, computed %q, pinned %q",
			firstFragment.GetDigest(),
			wantDigest,
			redfishSemanticFragmentDigest,
		)
	}

	direct, err := implementation.SemanticFragment(context.Background())
	if err != nil {
		t.Fatalf("SemanticFragment() error = %v", err)
	}
	direct.Document[0] = '!'
	fresh, err := implementation.SemanticFragment(context.Background())
	if err != nil {
		t.Fatalf("second SemanticFragment() error = %v", err)
	}
	if !bytes.Equal(fresh.Document, redfishSemanticFragmentDocument) {
		t.Fatal("SemanticFragment() returned shared document bytes")
	}
}

func TestRedfishSemanticFragmentMatchesCompleteMetadata(t *testing.T) {
	var document semanticTestDocument
	if err := yaml.Unmarshal(redfishSemanticFragmentDocument, &document); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if document.Format != "kubling-semantic" || document.SchemaVersion != 1 ||
		document.Kind != "fragment" {
		t.Fatalf("document envelope = %#v", document)
	}
	if document.Metadata.Name != semanticmodel.Name ||
		document.Metadata.Namespace != semanticmodel.Namespace ||
		document.Metadata.Version != redfishSemanticFragmentVersion ||
		redfishSemanticFragmentVersion != semanticmodel.Version {
		t.Fatalf("document metadata = %#v", document.Metadata)
	}

	metadata := catalog.Metadata(catalog.MetadataOptions{})
	tables := make(map[string]*providerv1.TableMetadata, len(metadata.GetTables()))
	for _, table := range metadata.GetTables() {
		tables[table.GetName()] = table
	}
	if len(document.Spec.Entities) != len(tables) {
		t.Fatalf("semantic entities = %d, metadata tables = %d", len(document.Spec.Entities), len(tables))
	}

	entities := make(map[string]semanticTestEntity, len(document.Spec.Entities))
	properties := make(map[string]map[string]semanticTestProperty, len(document.Spec.Entities))
	for _, entity := range document.Spec.Entities {
		if _, duplicate := entities[entity.ID]; duplicate {
			t.Fatalf("duplicate semantic entity %q", entity.ID)
		}
		if strings.Contains(entity.Binding.Relation, ".") {
			t.Fatalf("entity %s relation %q is qualified", entity.ID, entity.Binding.Relation)
		}
		table := tables[entity.Binding.Relation]
		if table == nil {
			t.Fatalf("entity %s binds unknown table %q", entity.ID, entity.Binding.Relation)
		}
		fields := make(map[string]*providerv1.ColumnMetadata, len(table.GetColumns()))
		var stableKey []string
		for _, column := range table.GetColumns() {
			if column.GetName() == catalog.IdentifierColumn {
				stableKey = column.GetStableKey().GetColumns()
				continue
			}
			fields[column.GetName()] = column
		}
		if len(entity.Properties) != len(fields) {
			t.Fatalf(
				"entity %s properties = %d, source fields = %d",
				entity.ID,
				len(entity.Properties),
				len(fields),
			)
		}
		propertyIndex := make(map[string]semanticTestProperty, len(entity.Properties))
		fieldToProperty := make(map[string]string, len(entity.Properties))
		for _, property := range entity.Properties {
			if strings.Contains(property.Binding.Field, ".") {
				t.Fatalf(
					"property %s.%s field %q is qualified",
					entity.ID,
					property.ID,
					property.Binding.Field,
				)
			}
			column := fields[property.Binding.Field]
			if column == nil {
				t.Fatalf(
					"property %s.%s binds unknown field %q",
					entity.ID,
					property.ID,
					property.Binding.Field,
				)
			}
			if property.Type != semanticColumnType(t, column) {
				t.Fatalf(
					"property %s.%s type = %q, want %q",
					entity.ID,
					property.ID,
					property.Type,
					semanticColumnType(t, column),
				)
			}
			if _, duplicate := propertyIndex[property.ID]; duplicate {
				t.Fatalf("entity %s has duplicate property %q", entity.ID, property.ID)
			}
			if _, duplicate := fieldToProperty[property.Binding.Field]; duplicate {
				t.Fatalf("entity %s binds field %q twice", entity.ID, property.Binding.Field)
			}
			propertyIndex[property.ID] = property
			fieldToProperty[property.Binding.Field] = property.ID
		}
		if len(entity.Identities) != 1 {
			t.Fatalf("entity %s identities = %v, want one", entity.ID, entity.Identities)
		}
		wantIdentity := make([]string, 0, len(stableKey))
		for _, field := range stableKey {
			propertyID, exists := fieldToProperty[field]
			if !exists {
				t.Fatalf("entity %s identity field %q is not bound", entity.ID, field)
			}
			wantIdentity = append(wantIdentity, propertyID)
		}
		if !slices.Equal(entity.Identities[0].Properties, wantIdentity) {
			t.Fatalf(
				"entity %s identity = %v, want %v",
				entity.ID,
				entity.Identities[0].Properties,
				wantIdentity,
			)
		}
		entities[entity.ID] = entity
		properties[entity.ID] = propertyIndex
		delete(tables, entity.Binding.Relation)
	}
	if len(tables) != 0 {
		t.Fatalf("metadata tables without semantic entities = %v", tables)
	}

	relationships := make(map[string]semanticTestRelationship, len(document.Spec.Relationships))
	for _, relationship := range document.Spec.Relationships {
		if _, duplicate := relationships[relationship.ID]; duplicate {
			t.Fatalf("duplicate semantic relationship %q", relationship.ID)
		}
		if _, exists := entities[relationship.From]; !exists {
			t.Fatalf("relationship %s source %q is missing", relationship.ID, relationship.From)
		}
		if _, exists := entities[relationship.To]; !exists {
			t.Fatalf("relationship %s target %q is missing", relationship.ID, relationship.To)
		}
		if len(relationship.Joins) == 0 {
			t.Fatalf("relationship %s is not executable: %v", relationship.ID, relationship.Joins)
		}
		var joinsNamespace bool
		var joinsDeviceID bool
		for _, join := range relationship.Joins {
			left := semanticJoinProperty(t, properties, relationship.From, join.Left)
			right := semanticJoinProperty(t, properties, relationship.To, join.Right)
			if left.Type != right.Type {
				t.Fatalf(
					"relationship %s joins incompatible types %s and %s",
					relationship.ID,
					left.Type,
					right.Type,
				)
			}
			joinsNamespace = joinsNamespace || left.ID == "namespace" && right.ID == "namespace"
			joinsDeviceID = joinsDeviceID || left.ID == "deviceId" && right.ID == "deviceId"
		}
		if !joinsNamespace {
			t.Fatalf("relationship %s can cross namespaces: %v", relationship.ID, relationship.Joins)
		}
		if relationship.ID != "NamespaceContainsDevices" && !joinsDeviceID {
			t.Fatalf("relationship %s can cross devices: %v", relationship.ID, relationship.Joins)
		}
		relationships[relationship.ID] = relationship
	}
	if len(relationships) != 25 {
		t.Fatalf("semantic relationships = %d, want 25", len(relationships))
	}
	for _, required := range []string{
		"NamespaceContainsDevices",
		"SystemHasChassisLinks",
		"ChassisHasSystemLinks",
		"StorageSubsystemHasDriveLinks",
		"DriveHasStorageLinks",
		"SoftwareInventoryHasTargetLinks",
		"ChassisHasPowerSubsystem",
		"ChassisHasEnvironmentMetrics",
		"ChassisHasPowerSupplies",
		"ChassisHasTemperatures",
		"ChassisHasFans",
	} {
		if _, exists := relationships[required]; !exists {
			t.Fatalf("required semantic relationship %q is missing", required)
		}
	}
	if relationship := relationships["NamespaceContainsDevices"]; relationship.Cardinality != "oneToMany" || len(relationship.Joins) != 1 {
		t.Fatalf("NamespaceContainsDevices = %#v", relationship)
	}
	if relationship := relationships["ChassisHasPowerSubsystem"]; relationship.Cardinality != "oneToOne" || len(relationship.Joins) != 3 {
		t.Fatalf("ChassisHasPowerSubsystem = %#v", relationship)
	}
	for _, relationshipID := range []string{
		"ChassisHasEnvironmentMetrics",
		"ChassisHasPowerSupplies",
		"ChassisHasTemperatures",
		"ChassisHasFans",
	} {
		if relationship := relationships[relationshipID]; relationship.Cardinality != "oneToMany" || len(relationship.Joins) != 3 {
			t.Fatalf("%s = %#v", relationshipID, relationship)
		}
	}
}

func TestRedfishSemanticFragmentPreservesCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newTestProvider(t).SemanticFragment(ctx); status.Code(err) != codes.Canceled {
		t.Fatalf("SemanticFragment() code = %v, want Canceled", status.Code(err))
	}
}

func TestRedfishSemanticFragmentGRPCIntegrationDoesNotContactBMC(t *testing.T) {
	source := testredfish.NewServer(t, testredfish.Options{
		ID:             "semantic-grpc",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
	})
	client := newGRPCTestClient(t, queryTestProvider(t, source))
	response, err := client.GetSemanticFragment(
		context.Background(),
		&providerv1.GetSemanticFragmentRequest{},
	)
	if err != nil {
		t.Fatalf("GetSemanticFragment() error = %v", err)
	}
	if response.GetFragment() == nil ||
		!bytes.Equal(response.GetFragment().GetDocument(), redfishSemanticFragmentDocument) {
		t.Fatalf("GetSemanticFragment() response = %v", response)
	}
	if len(source.Requests()) != 0 {
		t.Fatalf("gRPC semantic retrieval contacted the BMC: %v", source.Requests())
	}
}

func semanticColumnType(t *testing.T, column *providerv1.ColumnMetadata) string {
	t.Helper()
	switch column.GetType() {
	case kublingv1.ValueType_VALUE_TYPE_STRING:
		return "string"
	case kublingv1.ValueType_VALUE_TYPE_BOOLEAN:
		return "boolean"
	case kublingv1.ValueType_VALUE_TYPE_INTEGER:
		return "integer"
	case kublingv1.ValueType_VALUE_TYPE_LONG:
		return "long"
	case kublingv1.ValueType_VALUE_TYPE_BIGINTEGER:
		return "biginteger"
	case kublingv1.ValueType_VALUE_TYPE_DOUBLE:
		return "double"
	case kublingv1.ValueType_VALUE_TYPE_TIMESTAMP:
		return "timestamp"
	case kublingv1.ValueType_VALUE_TYPE_JSON:
		return "json"
	default:
		t.Fatalf("unsupported test semantic type %s", column.GetType())
		return ""
	}
}

func semanticJoinProperty(
	t *testing.T,
	properties map[string]map[string]semanticTestProperty,
	entityID string,
	reference string,
) semanticTestProperty {
	t.Helper()
	parts := strings.Split(reference, ".")
	if len(parts) != 2 || parts[0] != entityID {
		t.Fatalf("join reference %q does not belong to entity %q", reference, entityID)
	}
	property, exists := properties[entityID][parts[1]]
	if !exists {
		t.Fatalf("join reference %q names an unknown property", reference)
	}
	return property
}
