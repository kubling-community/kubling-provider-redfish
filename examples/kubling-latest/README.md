# Kubling latest Redfish sample

Run the provider from this checkout against two deterministic virtual HTTPS
Redfish BMCs, then query both devices through the current public Kubling images.
No published Redfish provider image, physical server or private credential is
required.

## Requirements

- Docker Engine
- Docker Compose v2
- outbound access to Docker Hub on the first run

## One-command verification

From the repository root, run:

```sh
make sample
```

The command always resolves the external `latest` tags, builds this provider
and the two BMCs locally, waits for Kubling, executes SQL assertions, and then
removes the sample containers, network, descriptor volume and local images.
The public images remain in Docker's cache.

The expected final line is:

```text
Kubling latest Redfish sample passed: namespace discovery, 2 BMCs, inventory, routing, power and thermal SQL.
```

## Keep the sample running

Start the same stack without automatic cleanup:

```sh
make sample-up
```

Open Kubling Studio at <http://localhost:8282/console>. The login form requires
non-empty values even though this VDB has no authentication delegate; `sample`
can be used for both fields. Select `RedfishSampleVDB` in the SQL Workspace.

Run the automated assertions against the live stack and stop it when finished:

```sh
make sample-test
make sample-down
```

Set `KUBLING_HTTP_PORT` before `make sample-up` if port 8282 is already in use.
`KUBLING_IMAGE` is an optional diagnostic override; when unset, the sample uses
`docker.io/kubling/kubling:latest`.

## What is emulated

Both BMCs implement the Redfish resources needed for systems, chassis, modern
power subsystems, environment metrics, power supplies, temperature sensors and
fans. Their identities and readings are intentionally different:

| Device | Power | Temperature | Humidity | Fan |
|:--|--:|--:|--:|--:|
| `bmc-a` | 248.5 W | 27.25 C | 41% | 4800 RPM |
| `bmc-b` | 312.75 W | 31.5 C | 46.5% | 5600 RPM |

The provider is configured with `allowUnboundedFanout: true` only for this
two-device sample, so fleet queries can demonstrate fan-out. The automated
check also constrains one query by `namespace` and `device_id` to prove routed
access.

Example fleet queries:

```sql
SELECT namespace, device_count
FROM redfish.NAMESPACES
ORDER BY namespace;

SELECT device_id, name, manufacturer, model, power_state
FROM redfish.SYSTEMS
ORDER BY device_id;

SELECT device_id, power_watts, temperature_celsius, humidity_percent
FROM redfish.CHASSIS_ENVIRONMENT_METRICS
ORDER BY device_id;

SELECT device_id, name, capacity_watts, allocated_watts, status_health
FROM redfish.POWER_SUBSYSTEMS
ORDER BY device_id;

SELECT device_id, name, manufacturer, capacity_watts, status_health
FROM redfish.POWER_SUPPLIES
ORDER BY device_id;

SELECT device_id, name, reading_celsius, upper_threshold_critical
FROM redfish.TEMPERATURES
ORDER BY device_id;

SELECT device_id, name, speed_rpm, speed_percent
FROM redfish.FANS
ORDER BY device_id;
```

## Runtime contract

| Component | Source |
|:--|:--|
| Kubling | `docker.io/kubling/kubling:latest` |
| Descriptor builder | `docker.io/kubling/kubling-cli:latest` |
| Provider health probe | `docker.io/fullstorydev/grpcurl:latest` |
| HTTP readiness and SQL test | `docker.io/curlimages/curl:latest` |
| Redfish provider | local production `Dockerfile` |
| Virtual BMCs | local `test/container/Dockerfile` mock target |

Compose uses `pull_policy: always` for every public image so the sample tests
the current upstream tags rather than silently reusing an older cached tag.
Kubling registers the local provider as `PROVIDER_GRPC`; the VDB contains no
table DDL because Kubling imports the provider-owned 22-table schema through
`GetSchema`.

The synthetic BMC certificates are generated at container startup. TLS
verification is disabled only for these isolated fixtures; real device
configuration remains verify-by-default.
