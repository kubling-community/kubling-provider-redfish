# Security Policy

## Supported versions

Kubling Redfish Provider is currently pre-1.0. Security fixes are applied to
the latest release. Older versions may require an upgrade to receive a fix.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability.

Use GitHub's private vulnerability reporting feature in the repository's
Security tab. Include:

- the affected version and configuration;
- steps to reproduce or a proof of concept;
- the potential impact;
- any known mitigations.

If private vulnerability reporting is unavailable, open an issue containing no
vulnerability details and ask the maintainers to provide a private reporting
channel.

Reports are reviewed on a best-effort basis. We will acknowledge the report,
investigate it and coordinate disclosure when a fix is available. Please avoid
accessing data that is not yours or disrupting systems while researching an
issue.

## Scope

This policy covers the provider process, Redfish connectivity, configuration,
generated data model and release automation in this repository.
Vulnerabilities in Kubling, BMC firmware or another separately distributed
component should be reported through the security policy of the affected
project.

## Deployment baseline

The container image runs as a non-root user and does not include provider
configuration, device-specific trust anchors or credentials. Mount
configuration, CA bundles and file-backed secrets read-only, and keep secret
material outside the image and repository. Prefer a read-only container
filesystem and drop Linux capabilities unless the surrounding platform
requires otherwise.

The provider currently serves plaintext gRPC and does not authenticate callers
at its own listener. Bind it to a private workload network or place it behind
transport security and caller authentication. Do not expose the listener
directly to an untrusted network. TLS verification for BMC connections remains
enabled by default; `tls.insecureSkipVerify` is only an explicit compatibility
escape hatch and produces an operational warning.
