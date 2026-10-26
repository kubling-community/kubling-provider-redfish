# Contributing to Kubling Redfish Provider

Thank you for helping expose Redfish infrastructure through Kubling.
Contributions may be written by hand, with a coding agent, or with a mixture
of both. The tool does not change the acceptance bar: the person submitting
the change is responsible for understanding it, reviewing it and validating
its behavior.

## Before you start

The provider keeps Redfish-specific connectivity, metadata and execution
outside the Kubling runtime. It must advertise only capabilities it implements
completely and with exact semantics.

Treat the published Kubling provider SDK, the current DMTF Redfish schemas and
the Redfish specification as authoritative. Do not copy generated protocol
definitions into this repository or maintain the Redfish schema surface by
hand when it can be generated deterministically.

Never commit real BMC endpoints, credentials, session tokens, certificates,
customer inventory or environment-specific configuration. Tests must use
synthetic fixtures and local HTTP servers.

## Development workflow

Format and validate changes from the repository root:

```sh
go fmt ./...
go mod tidy -diff
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

The `make check` target runs formatting verification, vet, race-enabled tests
and the provider binary build. It also runs `make schema-check` and
`make semantic-check`, which fail when the generated Redfish catalog or its
source-local semantic fragment has drifted. Use `make schema` only after an
intentional schema or profile change, then run `make semantic` and review both
generated diffs.

Changes to runtime behavior, configuration, dependencies or packaging must
also pass the production-image E2E:

```sh
make container-e2e
```

It builds the provider image and queries it through gRPC while two synthetic
HTTPS Redfish services expose colliding resource URIs. No BMC, private service
or real credential is used. CI additionally builds the same Dockerfile for
`linux/amd64` and `linux/arm64`; neither pull-request nor branch CI publishes
an image.

Catalog graph behavior belongs in the declarative profile, not in
table-specific query code. Every traversal step, projection and type must
resolve against a schema recorded in `schema/upstream.lock.json`. When a modern
and legacy Redfish representation are normalized into one table, test the
modern-only, legacy-only and mixed cases; fallback is evaluated independently
for each parent resource, and a modern result must prevent duplicate legacy
rows. Preserve official fragment-bearing identities for legacy embedded
members even though HTTP requests target the containing resource.

Work in small blocks and add focused tests with each behavior. Before opening a
pull request, verify that:

- capability declarations match implemented operations;
- process health remains separate from individual BMC health;
- TLS verification is enabled by default;
- credentials never appear in logs or errors;
- requests, retries, pagination and fan-out are bounded;
- cancellation and connection closure release source resources;
- equal resources from different devices cannot collide in identity;
- every generated table can be executed against a representative fixture;
- missing optional collections are empty while malformed advertised data fails;
- unsupported operations return an appropriate gRPC status;
- generated artifacts are reproducible and their check command detects drift;
- semantic entities, properties, identities and joins bind to published
  metadata without opening a logical connection or contacting a BMC;
- public examples contain no real secrets, endpoints or local paths;
- the production image still runs as a non-root user and contains no bundled
  configuration or credentials;
- the two-BMC container E2E passes and leaves no running project resources.

Use focused conventional commits and explain source-specific limitations in
the pull request. Changes should be reviewable without knowing which tools were
used to produce them.
