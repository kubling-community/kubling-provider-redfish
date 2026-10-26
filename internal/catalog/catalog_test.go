package catalog

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	"google.golang.org/protobuf/proto"
)

func TestGeneratedCatalogHasStableMVPAndInferredTypes(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if ProfileName != "inventory-core" || Namespace != "redfish" || DMTFRelease != "2026.2" ||
		DMTFCommit != "4f81814e399213c9055863ddaff42791c6b3706a" {
		t.Fatalf("catalog identity = %q %q %q %q", ProfileName, Namespace, DMTFRelease, DMTFCommit)
	}
	tables := Tables()
	gotNames := make([]string, 0, len(tables))
	for _, table := range tables {
		gotNames = append(gotNames, table.Name)
		if table.Name == "NAMESPACES" {
			if table.Discovery.Kind != DiscoveryRegistry || len(table.RoutingColumns) != 0 ||
				!reflect.DeepEqual(table.StableKeyColumns, []string{"namespace"}) {
				t.Fatalf("NAMESPACES descriptor = %#v", table)
			}
			assertCatalogColumn(t, table, "namespace", kublingv1.ValueType_VALUE_TYPE_STRING, false)
			assertCatalogColumn(t, table, "device_count", kublingv1.ValueType_VALUE_TYPE_LONG, false)
			continue
		}
		if !reflect.DeepEqual(table.RoutingColumns, []string{"namespace", "device_id"}) {
			t.Fatalf("%s routing = %v", table.Name, table.RoutingColumns)
		}
		assertCatalogColumn(t, table, "namespace", kublingv1.ValueType_VALUE_TYPE_STRING, false)
		assertCatalogColumn(t, table, "device_id", kublingv1.ValueType_VALUE_TYPE_STRING, false)
		assertCatalogColumn(t, table, "observed_at", kublingv1.ValueType_VALUE_TYPE_TIMESTAMP, false)
		if strings.HasSuffix(table.Name, "_DRIVES") || table.Name == "SYSTEM_CHASSIS" ||
			table.Name == "SYSTEM_MANAGERS" || table.Name == "SOFTWARE_INVENTORY_TARGETS" {
			if !reflect.DeepEqual(table.StableKeyColumns, []string{"namespace", "device_id", "source_uri", "target_uri"}) {
				t.Fatalf("%s stable key = %v", table.Name, table.StableKeyColumns)
			}
			assertCatalogColumn(t, table, "source_uri", kublingv1.ValueType_VALUE_TYPE_STRING, false)
			assertCatalogColumn(t, table, "target_uri", kublingv1.ValueType_VALUE_TYPE_STRING, false)
		} else {
			if !reflect.DeepEqual(table.StableKeyColumns, []string{"namespace", "device_id", "resource_uri"}) {
				t.Fatalf("%s stable key = %v", table.Name, table.StableKeyColumns)
			}
			assertCatalogColumn(t, table, "resource_uri", kublingv1.ValueType_VALUE_TYPE_STRING, false)
			assertCatalogColumn(t, table, "redfish_id", kublingv1.ValueType_VALUE_TYPE_STRING, false)
		}
	}
	wantNames := []string{
		"NAMESPACES", "DEVICES", "SYSTEMS", "PROCESSORS", "MEMORY_MODULES", "STORAGE_SUBSYSTEMS",
		"STORAGE_CONTROLLERS", "DRIVES", "ETHERNET_INTERFACES", "SOFTWARE_INVENTORY",
		"CHASSIS", "MANAGERS", "SYSTEM_CHASSIS", "SYSTEM_MANAGERS", "STORAGE_DRIVES",
		"CHASSIS_DRIVES", "SOFTWARE_INVENTORY_TARGETS", "POWER_SUBSYSTEMS",
		"CHASSIS_ENVIRONMENT_METRICS", "POWER_SUPPLIES", "TEMPERATURES", "FANS",
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("table order = %v", gotNames)
	}
	chassis, ok := LookupTable("chassis")
	if !ok {
		t.Fatal("LookupTable(CHASSIS) not found")
	}
	assertCatalogColumn(t, chassis, "chassis_type", kublingv1.ValueType_VALUE_TYPE_STRING, false)
	assertCatalogColumn(t, chassis, "status_state", kublingv1.ValueType_VALUE_TYPE_STRING, true)
	processors, _ := LookupTable("PROCESSORS")
	assertCatalogColumn(t, processors, "total_cores", kublingv1.ValueType_VALUE_TYPE_LONG, true)
	memory, _ := LookupTable("MEMORY_MODULES")
	assertCatalogColumn(t, memory, "capacity_mib", kublingv1.ValueType_VALUE_TYPE_LONG, true)
	metrics, _ := LookupTable("CHASSIS_ENVIRONMENT_METRICS")
	assertCatalogColumn(t, metrics, "power_watts", kublingv1.ValueType_VALUE_TYPE_DOUBLE, true)
	temperatures, _ := LookupTable("TEMPERATURES")
	assertCatalogColumn(t, temperatures, "reading_celsius", kublingv1.ValueType_VALUE_TYPE_DOUBLE, true)
	fans, _ := LookupTable("FANS")
	assertCatalogColumn(t, fans, "speed_rpm", kublingv1.ValueType_VALUE_TYPE_DOUBLE, true)
	if metrics.Discovery.Kind != DiscoveryGraph ||
		metrics.Discovery.Routes[0].Mode != TraversalFallback ||
		len(metrics.Discovery.Routes[0].Branches) != 2 {
		t.Fatalf("metrics traversal = %#v", metrics.Discovery)
	}

	chassis.Columns[0].Name = "mutated"
	chassis.StableKeyColumns[0] = "mutated"
	fresh, _ := LookupTable("CHASSIS")
	if fresh.Columns[0].Name != "namespace" || fresh.StableKeyColumns[0] != "namespace" {
		t.Fatal("LookupTable() returned shared generated state")
	}
}

func TestValidateTableRejectsDescriptorShapeDrift(t *testing.T) {
	device, ok := LookupTable("DEVICES")
	if !ok {
		t.Fatal("LookupTable(DEVICES) not found")
	}
	device.Discovery.Routes = []TraversalRoute{{Mode: TraversalUnion}}
	if err := validateTable(device); err == nil || !strings.Contains(err.Error(), "singleton discovery") {
		t.Fatalf("validateTable(singleton routes) error = %v", err)
	}

	device, _ = LookupTable("DEVICES")
	device.Columns[0].Source = ColumnSource("invented")
	if err := validateTable(device); err == nil || !strings.Contains(err.Error(), "invalid source") {
		t.Fatalf("validateTable(column source) error = %v", err)
	}

	namespaces, ok := LookupTable("NAMESPACES")
	if !ok {
		t.Fatal("LookupTable(NAMESPACES) not found")
	}
	namespaces.RoutingColumns = []string{"namespace"}
	if err := validateTable(namespaces); err == nil || !strings.Contains(err.Error(), "must not declare routing") {
		t.Fatalf("validateTable(registry routing) error = %v", err)
	}

	namespaces, _ = LookupTable("NAMESPACES")
	namespaces.Columns[1].Source = ColumnSourceDeviceID
	if err := validateTable(namespaces); err == nil || !strings.Contains(err.Error(), "invalid source") {
		t.Fatalf("validateTable(registry source) error = %v", err)
	}
}

func TestMetadataDeclaresStableIdentityAndRoutingDeterministically(t *testing.T) {
	metadata := Metadata(MetadataOptions{})
	if len(metadata.GetNamespaces()) != 1 || metadata.GetNamespaces()[0].GetName() != "redfish" {
		t.Fatalf("metadata namespaces = %v", metadata.GetNamespaces())
	}
	if len(metadata.GetTables()) != 22 || metadata.GetProperties()["redfish.dmtf.release"] != "2026.2" ||
		metadata.GetProperties()["redfish.table_count"] != "22" {
		t.Fatalf("metadata identity = %v", metadata)
	}
	descriptors := make(map[string]Table)
	for _, descriptor := range Tables() {
		descriptors[descriptor.Name] = descriptor
	}
	for _, table := range metadata.GetTables() {
		descriptor := descriptors[table.GetName()]
		validateMetadataTable(t, table, descriptor.StableKeyColumns, len(descriptor.RoutingColumns) != 0)
		if table.GetName() == "NAMESPACES" &&
			(table.GetProperties()["redfish.discovery"] != "registry" ||
				table.GetProperties()["redfish.schema"] != "" ||
				table.GetProperties()["redfish.definition"] != "") {
			t.Fatalf("NAMESPACES properties = %v", table.GetProperties())
		}
	}

	unbounded := Metadata(MetadataOptions{AllowUnboundedFanout: true})
	for _, table := range unbounded.GetTables() {
		validateMetadataTable(t, table, descriptors[table.GetName()].StableKeyColumns, false)
	}

	marshal := proto.MarshalOptions{Deterministic: true}
	first, err := marshal.Marshal(metadata)
	if err != nil {
		t.Fatalf("Marshal(first) error = %v", err)
	}
	second, err := marshal.Marshal(Metadata(MetadataOptions{}))
	if err != nil {
		t.Fatalf("Marshal(second) error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("metadata bytes are not deterministic")
	}

	metadata.Tables[0].Name = "MUTATED"
	if Metadata(MetadataOptions{}).GetTables()[0].GetName() == "MUTATED" {
		t.Fatal("Metadata() returned shared state")
	}
}

func validateMetadataTable(
	t *testing.T,
	table *providerv1.TableMetadata,
	stableKey []string,
	expectRouting bool,
) {
	t.Helper()
	if table.GetNamespace() != "redfish" || table.GetKind() != providerv1.TableKind_TABLE_KIND_TABLE || table.GetUpdatable() {
		t.Fatalf("table %s base metadata = %v", table.GetName(), table)
	}
	columns := make(map[string]*providerv1.ColumnMetadata, len(table.GetColumns()))
	for _, column := range table.GetColumns() {
		if column == nil || column.GetName() == "" {
			t.Fatalf("table %s has invalid column %v", table.GetName(), column)
		}
		if _, exists := columns[column.GetName()]; exists {
			t.Fatalf("table %s duplicates column %s", table.GetName(), column.GetName())
		}
		if column.GetType() == kublingv1.ValueType_VALUE_TYPE_UNKNOWN ||
			(column.TypeDescriptor != nil && column.GetTypeDescriptor().GetType() != column.GetType()) {
			t.Fatalf("table %s column %s type = %v", table.GetName(), column.GetName(), column)
		}
		columns[column.GetName()] = column
	}
	identifier := columns[IdentifierColumn]
	if identifier == nil || identifier.GetStableKey().GetFormat() != providerv1.StableKeyFormat_STABLE_KEY_FORMAT_VAL_PK_V1 ||
		!reflect.DeepEqual(identifier.GetStableKey().GetColumns(), stableKey) {
		t.Fatalf("table %s identifier = %v", table.GetName(), identifier)
	}
	if len(table.GetKeys()) != 2 || table.GetKeys()[0].GetKind() != providerv1.KeyKind_KEY_KIND_PRIMARY ||
		!reflect.DeepEqual(table.GetKeys()[0].GetColumns(), []string{IdentifierColumn}) ||
		table.GetKeys()[1].GetKind() != providerv1.KeyKind_KEY_KIND_UNIQUE ||
		!reflect.DeepEqual(table.GetKeys()[1].GetColumns(), stableKey) {
		t.Fatalf("table %s keys = %v", table.GetName(), table.GetKeys())
	}
	if expectRouting {
		if len(table.GetAccessPatterns()) != 2 ||
			!reflect.DeepEqual(table.GetAccessPatterns()[0].GetColumns(), []string{"namespace"}) ||
			!reflect.DeepEqual(table.GetAccessPatterns()[1].GetColumns(), []string{"device_id"}) {
			t.Fatalf("table %s access patterns = %v", table.GetName(), table.GetAccessPatterns())
		}
	} else if len(table.GetAccessPatterns()) != 0 {
		t.Fatalf("table %s access patterns = %v, want none", table.GetName(), table.GetAccessPatterns())
	}
	for _, name := range []string{"namespace", "device_id"} {
		if columns[name] != nil && columns[name].GetSearchability() != providerv1.ColumnSearchability_COLUMN_SEARCHABILITY_EQUALITY {
			t.Fatalf("table %s column %s is not equality searchable", table.GetName(), name)
		}
	}
	if resourceURI := columns["resource_uri"]; resourceURI != nil &&
		resourceURI.GetSearchability() != providerv1.ColumnSearchability_COLUMN_SEARCHABILITY_EQUALITY {
		t.Fatalf("table %s resource_uri is not equality searchable", table.GetName())
	}
}

func assertCatalogColumn(
	t *testing.T,
	table Table,
	name string,
	typeName kublingv1.ValueType,
	nullable bool,
) {
	t.Helper()
	for _, column := range table.Columns {
		if column.Name == name {
			if column.Type != typeName || column.Nullable != nullable {
				t.Fatalf("%s.%s = type %s nullable %v", table.Name, name, column.Type, column.Nullable)
			}
			return
		}
	}
	t.Fatalf("%s.%s is missing", table.Name, name)
}
