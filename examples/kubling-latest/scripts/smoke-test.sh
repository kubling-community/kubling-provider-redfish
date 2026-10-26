#!/bin/sh

set -eu

kubling_base_url="${KUBLING_BASE_URL:-http://kubling:8282}"
kubling_vdb="${KUBLING_VDB:-RedfishSampleVDB}"

fail() {
  printf 'ERROR: %s\n' "$1" >&2
  exit 1
}

assert_contains() {
  value="$1"
  expected="$2"
  description="$3"

  case "${value}" in
    *"${expected}"*) ;;
    *) fail "${description}; expected ${expected} in ${value}" ;;
  esac
}

assert_not_contains() {
  value="$1"
  unexpected="$2"
  description="$3"

  case "${value}" in
    *"${unexpected}"*) fail "${description}; did not expect ${unexpected} in ${value}" ;;
    *) ;;
  esac
}

encode_query() {
  printf '%s' "$1" |
    base64 |
    tr -d '\n' |
    sed -e 's/+/%2B/g' -e 's#/#%2F#g' -e 's/=/%3D/g'
}

execute_sql() {
  query="$1"
  encoded_query="$(encode_query "${query}")"
  curl --fail --silent --show-error \
    "${kubling_base_url}/api/v1/admin/query/${kubling_vdb}/${encoded_query}"
}

studio_status="$(
  curl --silent --show-error --location \
    --output /dev/null \
    --write-out '%{http_code}' \
    "${kubling_base_url}/console"
)"
if [ "${studio_status}" != "200" ]; then
  fail "Kubling Studio returned HTTP ${studio_status}, expected 200"
fi

namespaces="$(execute_sql "SELECT namespace, device_count FROM redfish.NAMESPACES ORDER BY namespace")"
assert_contains "${namespaces}" '"containsRows":true' "namespace registry query returned no rows"
assert_contains "${namespaces}" '"namespace":"sample"' "sample namespace is missing"
assert_contains "${namespaces}" '"device_count":"2"' "sample namespace device count is incorrect"

systems="$(execute_sql "SELECT device_id, redfish_id, name, manufacturer, model, power_state FROM redfish.SYSTEMS ORDER BY device_id")"
assert_contains "${systems}" '"containsRows":true' "system query returned no rows"
assert_contains "${systems}" '"device_id":"bmc-a"' "bmc-a system is missing"
assert_contains "${systems}" '"name":"Synthetic Server bmc-a"' "bmc-a system name is incorrect"
assert_contains "${systems}" '"device_id":"bmc-b"' "bmc-b system is missing"
assert_contains "${systems}" '"name":"Synthetic Server bmc-b"' "bmc-b system name is incorrect"
assert_contains "${systems}" '"manufacturer":"Kubling"' "system manufacturer is missing"
assert_contains "${systems}" '"power_state":"On"' "system power state is missing"

routed="$(execute_sql "SELECT device_id, name FROM redfish.SYSTEMS WHERE namespace = 'sample' AND device_id = 'bmc-a'")"
assert_contains "${routed}" '"containsRows":true' "routed system query returned no rows"
assert_contains "${routed}" '"device_id":"bmc-a"' "routed system query lost bmc-a"
assert_not_contains "${routed}" '"device_id":"bmc-b"' "routed system query reached bmc-b"

metrics="$(execute_sql "SELECT device_id, model_source, power_watts, temperature_celsius, humidity_percent FROM redfish.CHASSIS_ENVIRONMENT_METRICS ORDER BY device_id")"
assert_contains "${metrics}" '"containsRows":true' "environment-metrics query returned no rows"
assert_contains "${metrics}" '"power_watts":"248.5"' "bmc-a power reading is incorrect"
assert_contains "${metrics}" '"power_watts":"312.75"' "bmc-b power reading is incorrect"
assert_contains "${metrics}" '"temperature_celsius":"27.25"' "bmc-a environment temperature is incorrect"
assert_contains "${metrics}" '"temperature_celsius":"31.5"' "bmc-b environment temperature is incorrect"
assert_contains "${metrics}" '"humidity_percent":"41.0"' "bmc-a humidity reading is incorrect"
assert_contains "${metrics}" '"humidity_percent":"46.5"' "bmc-b humidity reading is incorrect"
assert_contains "${metrics}" '"model_source":"modern"' "modern environment model was not selected"

power_subsystems="$(execute_sql "SELECT device_id, name, capacity_watts, allocated_watts, status_health FROM redfish.POWER_SUBSYSTEMS ORDER BY device_id")"
assert_contains "${power_subsystems}" '"containsRows":true' "power-subsystem query returned no rows"
assert_contains "${power_subsystems}" '"name":"Synthetic Power Subsystem bmc-a"' "bmc-a power subsystem is missing"
assert_contains "${power_subsystems}" '"name":"Synthetic Power Subsystem bmc-b"' "bmc-b power subsystem is missing"
assert_contains "${power_subsystems}" '"capacity_watts":"1600.0"' "power-subsystem capacity is incorrect"
assert_contains "${power_subsystems}" '"allocated_watts":"248.5"' "bmc-a allocated power is incorrect"
assert_contains "${power_subsystems}" '"allocated_watts":"312.75"' "bmc-b allocated power is incorrect"
assert_contains "${power_subsystems}" '"status_health":"OK"' "power-subsystem health is missing"

power_supplies="$(execute_sql "SELECT device_id, name, manufacturer, capacity_watts, status_health FROM redfish.POWER_SUPPLIES ORDER BY device_id")"
assert_contains "${power_supplies}" '"containsRows":true' "power-supply query returned no rows"
assert_contains "${power_supplies}" '"name":"Synthetic PSU bmc-a"' "bmc-a power supply is missing"
assert_contains "${power_supplies}" '"name":"Synthetic PSU bmc-b"' "bmc-b power supply is missing"
assert_contains "${power_supplies}" '"manufacturer":"Kubling"' "power-supply manufacturer is missing"
assert_contains "${power_supplies}" '"capacity_watts":"1600.0"' "power-supply capacity is incorrect"
assert_contains "${power_supplies}" '"status_health":"OK"' "power-supply health is missing"

temperatures="$(execute_sql "SELECT device_id, name, reading_celsius, upper_threshold_critical FROM redfish.TEMPERATURES ORDER BY device_id")"
assert_contains "${temperatures}" '"containsRows":true' "temperature query returned no rows"
assert_contains "${temperatures}" '"name":"CPU Temperature bmc-a"' "bmc-a temperature sensor is missing"
assert_contains "${temperatures}" '"name":"CPU Temperature bmc-b"' "bmc-b temperature sensor is missing"
assert_contains "${temperatures}" '"reading_celsius":"27.25"' "bmc-a temperature sensor reading is incorrect"
assert_contains "${temperatures}" '"reading_celsius":"31.5"' "bmc-b temperature sensor reading is incorrect"
assert_contains "${temperatures}" '"upper_threshold_critical":"90.0"' "temperature threshold is missing"

fans="$(execute_sql "SELECT device_id, name, speed_rpm, speed_percent FROM redfish.FANS ORDER BY device_id")"
assert_contains "${fans}" '"containsRows":true' "fan query returned no rows"
assert_contains "${fans}" '"name":"Synthetic Fan bmc-a"' "bmc-a fan is missing"
assert_contains "${fans}" '"name":"Synthetic Fan bmc-b"' "bmc-b fan is missing"
assert_contains "${fans}" '"speed_rpm":"4800.0"' "bmc-a fan speed is incorrect"
assert_contains "${fans}" '"speed_rpm":"5600.0"' "bmc-b fan speed is incorrect"

printf 'Kubling latest Redfish sample passed: namespace discovery, 2 BMCs, inventory, routing, power and thermal SQL.\n'
