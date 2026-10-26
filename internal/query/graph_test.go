package query

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	kublingv1 "github.com/kubling-community/kubling-grpc/sdk-go/kubling/v1"
	"github.com/kubling-community/kubling-provider-redfish/internal/catalog"
	"github.com/kubling-community/kubling-provider-redfish/internal/testredfish"
	providerv1 "github.com/kubling-community/kubling-providers/sdk-go/kubling/provider/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGeneratedGraphTraversalInventoryRelationsAndAbsentCollections(t *testing.T) {
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "inventory",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
		ServiceRoot: map[string]any{
			"UpdateService": map[string]string{"@odata.id": "/redfish/v1/UpdateService"},
		},
		Resources: map[string]json.RawMessage{
			"/redfish/v1/Systems": rawJSON(`{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}`),
			"/redfish/v1/Systems/1": rawJSON(`{
				"@odata.id":"/redfish/v1/Systems/1",
				"Processors":{"@odata.id":"/redfish/v1/Systems/1/Processors"},
				"Memory":{"@odata.id":"/redfish/v1/Systems/1/Memory"},
				"Storage":{"@odata.id":"/redfish/v1/Systems/1/Storage"},
				"EthernetInterfaces":{"@odata.id":"/redfish/v1/Systems/1/EthernetInterfaces"},
				"Links":{
					"Chassis":[{"@odata.id":"/redfish/v1/Chassis/1"}],
					"ManagedBy":[{"@odata.id":"/redfish/v1/Managers/1"}]
				}
			}`),
			"/redfish/v1/Systems/1/Processors": rawJSON(`{"Members":[{
				"@odata.id":"/redfish/v1/Systems/1/Processors/CPU1",
				"@odata.type":"#Processor.v1_24_0.Processor",
				"Id":"CPU1","Name":"CPU 1","Model":"Vendor CPU","TotalCores":32
			}]}`),
			"/redfish/v1/Systems/1/Memory": rawJSON(`{"Members":[{
				"@odata.id":"/redfish/v1/Systems/1/Memory/DIMM1",
				"@odata.type":"#Memory.v1_24_0.Memory",
				"Id":"DIMM1","Name":"DIMM 1","CapacityMiB":32768
			}]}`),
			"/redfish/v1/Systems/1/Storage": rawJSON(`{"Members":[
				{
					"@odata.id":"/redfish/v1/Systems/1/Storage/modern",
					"@odata.type":"#Storage.v1_23_0.Storage",
					"Id":"modern","Name":"Modern storage",
					"Controllers":{"@odata.id":"/redfish/v1/Systems/1/Storage/modern/Controllers"},
					"StorageControllers":[{"@odata.id":"/redfish/v1/Systems/1/Storage/modern#/StorageControllers/0","MemberId":"duplicate","Name":"Legacy duplicate"}],
					"Drives":[{"@odata.id":"/redfish/v1/Drives/1"}]
				},
				{
					"@odata.id":"/redfish/v1/Systems/1/Storage/legacy",
					"@odata.type":"#Storage.v1_23_0.Storage",
					"Id":"legacy","Name":"Legacy storage",
					"StorageControllers":[{"@odata.id":"/redfish/v1/Systems/1/Storage/legacy#/StorageControllers/0","MemberId":"0","Name":"Legacy controller","FirmwareVersion":"1.0"}]
				}
			]}`),
			"/redfish/v1/Systems/1/Storage/modern/Controllers": rawJSON(`{"Members":[{
				"@odata.id":"/redfish/v1/Systems/1/Storage/modern/Controllers/1",
				"@odata.type":"#StorageController.v1_12_0.StorageController",
				"Id":"1","Name":"Modern controller","FirmwareVersion":"2.0"
			}]}`),
			"/redfish/v1/Drives/1": rawJSON(`{
				"@odata.id":"/redfish/v1/Drives/1","@odata.type":"#Drive.v1_22_0.Drive",
				"Id":"1","Name":"Drive 1","Model":"Vendor SSD","CapacityBytes":1000000000000
			}`),
			"/redfish/v1/Systems/1/EthernetInterfaces": rawJSON(`{"Members":[{
				"@odata.id":"/redfish/v1/Systems/1/EthernetInterfaces/1",
				"@odata.type":"#EthernetInterface.v1_12_6.EthernetInterface",
				"Id":"1","Name":"Host NIC","MACAddress":"02:00:00:00:00:01"
			}]}`),
			"/redfish/v1/Chassis": rawJSON(`{"Members":[{"@odata.id":"/redfish/v1/Chassis/1"}]}`),
			"/redfish/v1/Chassis/1": rawJSON(`{
				"@odata.id":"/redfish/v1/Chassis/1",
				"Drives":{"@odata.id":"/redfish/v1/Chassis/1/Drives"}
			}`),
			"/redfish/v1/Chassis/1/Drives": rawJSON(`{"Members":[{"@odata.id":"/redfish/v1/Drives/1"}]}`),
			"/redfish/v1/Managers":         rawJSON(`{"Members":[{"@odata.id":"/redfish/v1/Managers/1"}]}`),
			"/redfish/v1/Managers/1": rawJSON(`{
				"@odata.id":"/redfish/v1/Managers/1","Id":"1","Name":"BMC",
				"EthernetInterfaces":{"@odata.id":"/redfish/v1/Managers/1/EthernetInterfaces"}
			}`),
			"/redfish/v1/Managers/1/EthernetInterfaces": rawJSON(`{"Members":[{
				"@odata.id":"/redfish/v1/Managers/1/EthernetInterfaces/1",
				"@odata.type":"#EthernetInterface.v1_12_6.EthernetInterface",
				"Id":"1","Name":"BMC NIC","MACAddress":"02:00:00:00:00:02"
			}]}`),
			"/redfish/v1/UpdateService": rawJSON(`{
				"@odata.id":"/redfish/v1/UpdateService",
				"FirmwareInventory":{"@odata.id":"/redfish/v1/UpdateService/FirmwareInventory"}
			}`),
			"/redfish/v1/UpdateService/FirmwareInventory": rawJSON(`{"Members":[{
				"@odata.id":"/redfish/v1/UpdateService/FirmwareInventory/BMC",
				"@odata.type":"#SoftwareInventory.v1_15_0.SoftwareInventory",
				"Id":"BMC","Name":"BMC firmware","Version":"4.2.0",
				"RelatedItem":[{"@odata.id":"/redfish/v1/Managers/1"}]
			}]}`),
		},
	})

	processorRows := queryGraphRows(t, server, "PROCESSORS", "system_uri", "resource_uri", "model", "total_cores")
	if len(processorRows) != 1 || processorRows[0][0].GetStringValue() != "/redfish/v1/Systems/1" ||
		processorRows[0][1].GetStringValue() != "/redfish/v1/Systems/1/Processors/CPU1" ||
		processorRows[0][2].GetStringValue() != "Vendor CPU" || processorRows[0][3].GetLongValue() != 32 {
		t.Fatalf("processor rows = %v", processorRows)
	}
	memoryRows := queryGraphRows(t, server, "MEMORY_MODULES", "system_uri", "resource_uri", "capacity_mib")
	if len(memoryRows) != 1 || memoryRows[0][0].GetStringValue() != "/redfish/v1/Systems/1" ||
		memoryRows[0][1].GetStringValue() != "/redfish/v1/Systems/1/Memory/DIMM1" ||
		memoryRows[0][2].GetLongValue() != 32768 {
		t.Fatalf("memory rows = %v", memoryRows)
	}
	storageRows := queryGraphRows(t, server, "STORAGE_SUBSYSTEMS", "system_uri", "resource_uri", "name")
	if len(storageRows) != 2 {
		t.Fatalf("storage rows = %v", storageRows)
	}
	controllerRows := queryGraphRows(t, server, "STORAGE_CONTROLLERS", "storage_uri", "model_source", "name")
	assertNamedVariants(t, controllerRows, map[string]string{
		"modern": "Modern controller",
		"legacy": "Legacy controller",
	})
	driveRows := queryGraphRows(t, server, "DRIVES", "resource_uri", "model", "capacity_bytes")
	if len(driveRows) != 1 || driveRows[0][0].GetStringValue() != "/redfish/v1/Drives/1" ||
		driveRows[0][1].GetStringValue() != "Vendor SSD" || driveRows[0][2].GetLongValue() != 1000000000000 {
		t.Fatalf("drive rows = %v", driveRows)
	}
	interfaceRows := queryGraphRows(t, server, "ETHERNET_INTERFACES", "owner_uri", "owner_kind", "name")
	assertNamedVariants(t, interfaceRows, map[string]string{
		"system":  "Host NIC",
		"manager": "BMC NIC",
	})
	assertRelationRows(t, queryGraphRows(t, server, "SYSTEM_CHASSIS", "source_uri", "target_uri"),
		"/redfish/v1/Systems/1", "/redfish/v1/Chassis/1")
	assertRelationRows(t, queryGraphRows(t, server, "SYSTEM_MANAGERS", "source_uri", "target_uri"),
		"/redfish/v1/Systems/1", "/redfish/v1/Managers/1")
	assertRelationRows(t, queryGraphRows(t, server, "STORAGE_DRIVES", "source_uri", "target_uri"),
		"/redfish/v1/Systems/1/Storage/modern", "/redfish/v1/Drives/1")
	assertRelationRows(t, queryGraphRows(t, server, "CHASSIS_DRIVES", "source_uri", "target_uri"),
		"/redfish/v1/Chassis/1", "/redfish/v1/Drives/1")
	assertRelationRows(t, queryGraphRows(t, server, "SOFTWARE_INVENTORY_TARGETS", "source_uri", "target_uri"),
		"/redfish/v1/UpdateService/FirmwareInventory/BMC", "/redfish/v1/Managers/1")

	softwareRows := queryGraphRows(t, server, "SOFTWARE_INVENTORY", "inventory_kind", "version")
	if len(softwareRows) != 1 || softwareRows[0][0].GetStringValue() != "firmware" ||
		softwareRows[0][1].GetStringValue() != "4.2.0" {
		t.Fatalf("software rows = %v", softwareRows)
	}
}

func TestGeneratedGraphTraversalTreatsMissingOptionalCollectionAsEmpty(t *testing.T) {
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "optional-collection",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
		Resources: map[string]json.RawMessage{
			"/redfish/v1/Systems":   rawJSON(`{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}`),
			"/redfish/v1/Systems/1": rawJSON(`{"@odata.id":"/redfish/v1/Systems/1","Id":"1"}`),
		},
	})
	if rows := queryGraphRows(t, server, "MEMORY_MODULES", "resource_uri"); len(rows) != 0 {
		t.Fatalf("absent memory collection rows = %v", rows)
	}
}

func TestGeneratedGraphTraversalNormalizesModernLegacyPowerAndThermal(t *testing.T) {
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "power-thermal",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
		Resources: map[string]json.RawMessage{
			"/redfish/v1/Chassis": rawJSON(`{"Members":[
				{"@odata.id":"/redfish/v1/Chassis/modern"},
				{"@odata.id":"/redfish/v1/Chassis/legacy"}
			]}`),
			"/redfish/v1/Chassis/modern": rawJSON(`{
				"@odata.id":"/redfish/v1/Chassis/modern",
				"PowerSubsystem":{"@odata.id":"/redfish/v1/Chassis/modern/PowerSubsystem"},
				"EnvironmentMetrics":{"@odata.id":"/redfish/v1/Chassis/modern/EnvironmentMetrics"},
				"ThermalSubsystem":{"@odata.id":"/redfish/v1/Chassis/modern/ThermalSubsystem"},
				"Sensors":{"@odata.id":"/redfish/v1/Chassis/modern/Sensors"},
				"Power":{"@odata.id":"/redfish/v1/Chassis/modern/Power"},
				"Thermal":{"@odata.id":"/redfish/v1/Chassis/modern/Thermal"}
			}`),
			"/redfish/v1/Chassis/legacy": rawJSON(`{
				"@odata.id":"/redfish/v1/Chassis/legacy",
				"Power":{"@odata.id":"/redfish/v1/Chassis/legacy/Power"},
				"Thermal":{"@odata.id":"/redfish/v1/Chassis/legacy/Thermal"}
			}`),
			"/redfish/v1/Chassis/modern/PowerSubsystem": rawJSON(`{
				"@odata.id":"/redfish/v1/Chassis/modern/PowerSubsystem",
				"@odata.type":"#PowerSubsystem.v1_2_0.PowerSubsystem",
				"Id":"PowerSubsystem","Name":"Power subsystem","CapacityWatts":1600,
				"Status":{"State":"Enabled","Health":"OK"},
				"PowerSupplies":{"@odata.id":"/redfish/v1/Chassis/modern/PowerSubsystem/PowerSupplies"},
				"PowerSupplyRedundancy":[{"GroupName":"PSU pair","RedundancyType":"NPlusM","MinNeededInGroup":1,"Status":{"State":"Enabled"},"RedundancyGroup":[]}]
			}`),
			"/redfish/v1/Chassis/modern/PowerSubsystem/PowerSupplies": rawJSON(`{"Members":[{
				"@odata.id":"/redfish/v1/Chassis/modern/PowerSubsystem/PowerSupplies/1",
				"@odata.type":"#PowerSupply.v1_8_0.PowerSupply",
				"Id":"1","Name":"Modern PSU","PowerCapacityWatts":800,"Status":{"State":"Enabled","Health":"OK"}
			}]}`),
			"/redfish/v1/Chassis/modern/EnvironmentMetrics": rawJSON(`{
				"@odata.id":"/redfish/v1/Chassis/modern/EnvironmentMetrics",
				"@odata.type":"#EnvironmentMetrics.v1_7_0.EnvironmentMetrics",
				"Id":"EnvironmentMetrics","Name":"Environment metrics",
				"PowerWatts":{"Reading":321.5},"EnergykWh":{"Reading":12.25},
				"TemperatureCelsius":{"Reading":27.5},"HumidityPercent":{"Reading":42.0}
			}`),
			"/redfish/v1/Chassis/modern/ThermalSubsystem": rawJSON(`{
				"@odata.id":"/redfish/v1/Chassis/modern/ThermalSubsystem",
				"Fans":{"@odata.id":"/redfish/v1/Chassis/modern/ThermalSubsystem/Fans"}
			}`),
			"/redfish/v1/Chassis/modern/ThermalSubsystem/Fans": rawJSON(`{"Members":[{
				"@odata.id":"/redfish/v1/Chassis/modern/ThermalSubsystem/Fans/1",
				"@odata.type":"#Fan.v1_6_0.Fan","Id":"1","Name":"Modern fan",
				"SpeedPercent":{"Reading":55.0,"SpeedRPM":5500.0},"Status":{"State":"Enabled","Health":"OK"}
			}]}`),
			"/redfish/v1/Chassis/modern/Sensors": rawJSON(`{"Members":[
				{"@odata.id":"/redfish/v1/Chassis/modern/Sensors/Temp1","@odata.type":"#Sensor.v1_14_0.Sensor","Id":"Temp1","Name":"CPU temp","ReadingType":"Temperature","Reading":61.5,"Thresholds":{"UpperCritical":{"Reading":90.0}},"Status":{"State":"Enabled","Health":"OK"}},
				{"@odata.id":"/redfish/v1/Chassis/modern/Sensors/Volt1","@odata.type":"#Sensor.v1_14_0.Sensor","Id":"Volt1","Name":"Voltage","ReadingType":"Voltage","Reading":12.0}
			]}`),
			"/redfish/v1/Chassis/modern/Power": rawJSON(`{
				"@odata.id":"/redfish/v1/Chassis/modern/Power","@odata.type":"#Power.v1_7_3.Power","Id":"Power","Name":"Legacy duplicate",
				"PowerControl":[{"@odata.id":"/redfish/v1/Chassis/modern/Power#/PowerControl/0","MemberId":"0","PowerConsumedWatts":999}],
				"PowerSupplies":[{"@odata.id":"/redfish/v1/Chassis/modern/Power#/PowerSupplies/0","MemberId":"0","Name":"Legacy duplicate PSU"}]
			}`),
			"/redfish/v1/Chassis/modern/Thermal": rawJSON(`{
				"@odata.id":"/redfish/v1/Chassis/modern/Thermal","@odata.type":"#Thermal.v1_7_3.Thermal","Id":"Thermal","Name":"Legacy duplicate",
				"Temperatures":[{"@odata.id":"/redfish/v1/Chassis/modern/Thermal#/Temperatures/0","MemberId":"0","ReadingCelsius":99}],
				"Fans":[{"@odata.id":"/redfish/v1/Chassis/modern/Thermal#/Fans/0","MemberId":"0","Reading":99,"ReadingUnits":"Percent"}]
			}`),
			"/redfish/v1/Chassis/legacy/Power": rawJSON(`{
				"@odata.id":"/redfish/v1/Chassis/legacy/Power","@odata.type":"#Power.v1_7_3.Power","Id":"Power","Name":"Legacy power",
				"Redundancy":[],
				"PowerControl":[{"@odata.id":"/redfish/v1/Chassis/legacy/Power#/PowerControl/0","MemberId":"0","Name":"Chassis power","PowerConsumedWatts":210,"PowerMetrics":{"AverageConsumedWatts":200,"MinConsumedWatts":180,"MaxConsumedWatts":240,"IntervalInMin":5}}],
				"PowerSupplies":[{"@odata.id":"/redfish/v1/Chassis/legacy/Power#/PowerSupplies/0","MemberId":"0","Name":"Legacy PSU","PowerCapacityWatts":750,"Status":{"State":"Enabled","Health":"OK"}}]
			}`),
			"/redfish/v1/Chassis/legacy/Thermal": rawJSON(`{
				"@odata.id":"/redfish/v1/Chassis/legacy/Thermal","@odata.type":"#Thermal.v1_7_3.Thermal","Id":"Thermal","Name":"Legacy thermal",
				"Temperatures":[{"@odata.id":"/redfish/v1/Chassis/legacy/Thermal#/Temperatures/0","MemberId":"0","Name":"Inlet","ReadingCelsius":24.5,"UpperThresholdCritical":45,"Status":{"State":"Enabled","Health":"OK"}}],
				"Fans":[
					{"@odata.id":"/redfish/v1/Chassis/legacy/Thermal#/Fans/0","MemberId":"0","Name":"Legacy RPM fan","Reading":4200,"ReadingUnits":"RPM","Status":{"State":"Enabled","Health":"OK"}},
					{"@odata.id":"/redfish/v1/Chassis/legacy/Thermal#/Fans/1","MemberId":"1","Name":"Legacy percent fan","Reading":48,"ReadingUnits":"Percent","Status":{"State":"Enabled","Health":"OK"}}
				]
			}`),
		},
	})

	powerSubsystems := queryGraphRows(t, server, "POWER_SUBSYSTEMS", "chassis_uri", "model_source", "capacity_watts", "status_health", "redundancy")
	assertVariants(t, powerSubsystems, 2)
	for _, row := range powerSubsystems {
		switch row[1].GetStringValue() {
		case "modern":
			if row[2].GetDoubleValue() != 1600 || row[3].GetStringValue() != "OK" ||
				!strings.Contains(row[4].GetJsonValue(), `"GroupName":"PSU pair"`) {
				t.Fatalf("modern power subsystem row = %v", row)
			}
		case "legacy":
			if row[4].GetJsonValue() != "[]" {
				t.Fatalf("legacy power subsystem row = %v", row)
			}
		}
	}
	metrics := queryGraphRows(t, server, "CHASSIS_ENVIRONMENT_METRICS", "chassis_uri", "model_source", "power_watts", "energy_kwh", "temperature_celsius", "humidity_percent", "average_consumed_watts")
	assertModelReadings(t, metrics, map[string]float64{"modern": 321.5, "legacy": 210})
	for _, row := range metrics {
		switch row[1].GetStringValue() {
		case "modern":
			if row[3].GetDoubleValue() != 12.25 || row[4].GetDoubleValue() != 27.5 || row[5].GetDoubleValue() != 42 {
				t.Fatalf("modern environment row = %v", row)
			}
		case "legacy":
			if row[6].GetDoubleValue() != 200 {
				t.Fatalf("legacy environment row = %v", row)
			}
		}
	}
	powerSupplies := queryGraphRows(t, server, "POWER_SUPPLIES", "chassis_uri", "model_source", "name", "capacity_watts", "status_health")
	assertNamedVariants(t, powerSupplies, map[string]string{
		"modern": "Modern PSU",
		"legacy": "Legacy PSU",
	})
	for _, row := range powerSupplies {
		wantCapacity := 800.0
		if row[1].GetStringValue() == "legacy" {
			wantCapacity = 750
		}
		if row[3].GetDoubleValue() != wantCapacity || row[4].GetStringValue() != "OK" {
			t.Fatalf("power-supply row = %v", row)
		}
	}
	temperatures := queryGraphRows(t, server, "TEMPERATURES", "chassis_uri", "model_source", "reading_celsius", "upper_threshold_critical")
	assertModelReadings(t, temperatures, map[string]float64{"modern": 61.5, "legacy": 24.5})
	for _, row := range temperatures {
		wantThreshold := 90.0
		if row[1].GetStringValue() == "legacy" {
			wantThreshold = 45
		}
		if row[3].GetDoubleValue() != wantThreshold {
			t.Fatalf("temperature row = %v", row)
		}
	}
	embeddedRequest := graphRequest("TEMPERATURES", "resource_uri", "reading_celsius")
	embeddedRequest.Filter = and(
		equality("device_id", "bmc-01"),
		equality("resource_uri", "/redfish/v1/Chassis/legacy/Thermal#/Temperatures/0"),
	)
	embeddedRows := queryGraphRowsRequest(t, server, embeddedRequest)
	if len(embeddedRows) != 1 || embeddedRows[0][0].GetStringValue() != "/redfish/v1/Chassis/legacy/Thermal#/Temperatures/0" ||
		embeddedRows[0][1].GetDoubleValue() != 24.5 {
		t.Fatalf("embedded resource_uri rows = %v", embeddedRows)
	}
	fans := queryGraphRows(t, server, "FANS", "chassis_uri", "model_source", "speed_rpm", "speed_percent", "status_health")
	if len(fans) != 3 {
		t.Fatalf("fan rows = %v", fans)
	}
	seenModern := false
	seenLegacyRPM := false
	seenLegacyPercent := false
	allHealthy := true
	for _, row := range fans {
		allHealthy = allHealthy && row[4].GetStringValue() == "OK"
		switch {
		case row[1].GetStringValue() == "modern" && row[2].GetDoubleValue() == 5500:
			seenModern = true
		case row[1].GetStringValue() == "legacy" && row[2].GetDoubleValue() == 4200:
			seenLegacyRPM = true
		case row[1].GetStringValue() == "legacy" && row[3].GetDoubleValue() == 48:
			seenLegacyPercent = true
		}
	}
	if !seenModern || !seenLegacyRPM || !seenLegacyPercent || !allHealthy {
		t.Fatalf("normalized fan rows = %v", fans)
	}
}

func TestGeneratedGraphTraversalRejectsMistypedLinks(t *testing.T) {
	server := testredfish.NewServer(t, testredfish.Options{
		ID:             "mistyped",
		Username:       "operator",
		Password:       "password",
		Authentication: testredfish.AuthenticationBasic,
		Resources: map[string]json.RawMessage{
			"/redfish/v1/Systems":   rawJSON(`{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}`),
			"/redfish/v1/Systems/1": rawJSON(`{"@odata.id":"/redfish/v1/Systems/1","Processors":42}`),
		},
	})
	executor := queryTestExecutor(t, Config{})
	stream, err := executor.Query(
		context.Background(),
		querySnapshot(t, queryTestDevice(t, "dc-a", "bmc-01", server)),
		graphRequest("PROCESSORS", "resource_uri"),
	)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	defer stream.Close()
	if _, err := stream.Next(context.Background()); status.Code(err) != codes.DataLoss {
		t.Fatalf("Next() code = %v, error = %v", status.Code(err), err)
	}
}

func queryGraphRows(
	t *testing.T,
	server *testredfish.Server,
	table string,
	columns ...string,
) [][]*kublingv1.Value {
	t.Helper()
	return queryGraphRowsRequest(t, server, graphRequest(table, columns...))
}

func queryGraphRowsRequest(
	t *testing.T,
	server *testredfish.Server,
	request *providerv1.QueryRequest,
) [][]*kublingv1.Value {
	t.Helper()
	executor := queryTestExecutor(t, Config{})
	stream, err := executor.Query(
		context.Background(),
		querySnapshot(t, queryTestDevice(t, "dc-a", "bmc-01", server)),
		request,
	)
	if err != nil {
		t.Fatalf("Query(%s) error = %v", request.GetEntity().GetName(), err)
	}
	defer stream.Close()
	var rows [][]*kublingv1.Value
	for {
		batch, err := stream.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next(%s) error = %v", request.GetEntity().GetName(), err)
		}
		for _, tuple := range batch.GetTuples() {
			rows = append(rows, tuple.GetValues())
		}
	}
	return rows
}

func graphRequest(table string, columns ...string) *providerv1.QueryRequest {
	request := &providerv1.QueryRequest{
		Entity:    &providerv1.EntityReference{Name: table, Namespace: catalog.Namespace},
		BatchSize: uint32Pointer(2),
		Filter:    equality("device_id", "bmc-01"),
	}
	for _, column := range columns {
		request.Projections = append(request.Projections, fieldProjection(column, ""))
	}
	return request
}

func uint32Pointer(value uint32) *uint32 {
	return &value
}

func rawJSON(value string) json.RawMessage {
	return json.RawMessage(value)
}

func assertRelationRows(t *testing.T, rows [][]*kublingv1.Value, source string, target string) {
	t.Helper()
	if len(rows) != 1 || rows[0][0].GetStringValue() != source || rows[0][1].GetStringValue() != target {
		t.Fatalf("relationship rows = %v, want %s -> %s", rows, source, target)
	}
}

func assertVariants(t *testing.T, rows [][]*kublingv1.Value, count int) {
	t.Helper()
	if len(rows) != count {
		t.Fatalf("variant rows = %v", rows)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row[1].GetStringValue()] = true
	}
	if !seen["modern"] || !seen["legacy"] {
		t.Fatalf("variants = %v", rows)
	}
}

func assertNamedVariants(t *testing.T, rows [][]*kublingv1.Value, expected map[string]string) {
	t.Helper()
	if len(rows) != len(expected) {
		t.Fatalf("variant rows = %v", rows)
	}
	seen := make(map[string]string, len(rows))
	for _, row := range rows {
		seen[row[1].GetStringValue()] = row[2].GetStringValue()
	}
	for variant, name := range expected {
		if seen[variant] != name {
			t.Fatalf("variants = %v, want %v", seen, expected)
		}
	}
}

func assertModelReadings(t *testing.T, rows [][]*kublingv1.Value, expected map[string]float64) {
	t.Helper()
	if len(rows) != len(expected) {
		t.Fatalf("model rows = %v", rows)
	}
	for _, row := range rows {
		model := row[1].GetStringValue()
		if row[2].GetDoubleValue() != expected[model] {
			t.Fatalf("model row = %v, expected readings = %v", row, expected)
		}
	}
}
