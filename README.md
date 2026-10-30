# Redfish provider

The Redfish provider exposes multiple Redfish BMCs as one read-only Kubling
data source. Every row retains its configured `namespace`, stable `device_id`
and Redfish resource identity, so physical inventory can be joined safely with
other Kubling sources.

## Current implementation

The generated 22-table catalog covers systems, chassis, managers, processors,
memory, storage, drives, Ethernet interfaces, firmware, relationships, power
and thermal data. Modern Redfish power and thermal resources are preferred,
with per-chassis fallback to the legacy `Power` and `Thermal` resources.

`NAMESPACES` is a synthetic registry that can be queried without contacting a
BMC. BMC-backed tables route exact equality constraints on `namespace` and
`device_id`; `resource_uri` can further restrict a selected device. Unbounded
fan-out is disabled by default, and target count, concurrency, timeouts, pages,
rows, response size and retries are configurable.

Strict queries fail when any selected BMC fails. When partial results are
enabled by both Kubling and the provider, successful rows are retained and
target-specific failures are returned as structured warnings.

The catalog and bundled semantic fragment are generated from a locked subset
of the official DMTF Redfish schemas. The provider does not implement
mutations, Redfish actions, firmware updates, event subscriptions, historical
telemetry or log services.

## Configuration

See [`config.example.yaml`](config.example.yaml) for the complete multi-device
configuration. Credentials must come from environment variables or mounted
files; inline usernames and passwords are rejected. TLS verification is
enabled by default.

Start the provider with a local configuration:

```sh
go run ./cmd/kubling-provider-redfish -config ./config.local.yaml
```

The configuration path may also be supplied through
`KUBLING_REDFISH_CONFIG`. The default container path is
`/etc/kubling/provider.yaml`.

The provider exposes plaintext gRPC by default. Keep it on a trusted workload
network or place appropriate transport and caller controls in front of it.

## Debian package

Tagged releases attach packages for `amd64` and `arm64`. Download the matching
package from [project releases](https://github.com/kubling-community/kubling-provider-redfish/releases)
and install it with:

```sh
sudo apt install ./kubling-redfish-provider_*.deb
```

The package installs `/usr/bin/kubling-redfish-provider` and the example
configuration `/etc/kubling/redfish-provider.yaml.example`. It does not install
or start a service; configure its lifecycle and access to configuration,
credentials and CA files with the deployment's service manager.

## Querying

Assuming the Kubling schema is named `redfish`, discover the configured
namespaces first:

```sql
SELECT namespace, device_count
FROM redfish.NAMESPACES
ORDER BY namespace;
```

The registry can drive bounded joins across multiple namespaces without
hard-coding them:

```sql
SELECT ns.namespace,
       s.device_id,
       s.name,
       s.manufacturer,
       s.model,
       s.power_state,
       s.status_health
FROM redfish.NAMESPACES AS ns
JOIN redfish.SYSTEMS AS s
  ON s.namespace = ns.namespace
ORDER BY ns.namespace, s.device_id;
```

## Local sample

The [Kubling latest sample](examples/kubling-latest/README.md) builds this
provider, starts two synthetic HTTPS Redfish BMCs and validates inventory,
routing, power and thermal queries through the official Kubling images:

```sh
make sample
```

To keep the environment running for interactive use:

```sh
make sample-up
make sample-test
make sample-down
```

## Development

Go 1.26 or newer is required. Run the complete local verification:

```sh
make check
make container-e2e
```

Build the production container locally with:

```sh
docker build -t kubling-provider-redfish:local .
```

The catalog profile is declared in
[`schema/profiles/inventory-core.yaml`](schema/profiles/inventory-core.yaml).
Vendored schema provenance and update rules are documented in
[`schema/upstream/README.md`](schema/upstream/README.md).

After an intentional catalog change, regenerate and verify both artifacts:

```sh
make schema
make semantic
make schema-check
make semantic-check
```

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md), [`SECURITY.md`](SECURITY.md) and
[`SUPPORT.md`](SUPPORT.md).

## License

Apache License 2.0.
