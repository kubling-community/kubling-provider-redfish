# Debian package

Build one package from the repository root with Go and
[nFPM](https://nfpm.goreleaser.com/):

```sh
packaging/build-deb.sh 0.1.2 amd64
packaging/verify-deb.sh dist/kubling-redfish-provider_0.1.2_amd64.deb
```

Supported architectures are `amd64` and `arm64`. Packages are written to
`dist/` unless a third argument selects another output directory.
`GO_BINARY` and `NFPM_BINARY` may select executables that are not on `PATH`.

The package installs the provider binary, documentation and example
configuration. It does not install or start a service.
