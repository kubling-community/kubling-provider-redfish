#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <package.deb>" >&2
  exit 2
fi

package="$1"
if [[ ! -f "${package}" ]]; then
  echo "package not found: ${package}" >&2
  exit 1
fi

package_name="$(dpkg-deb --field "${package}" Package)"
package_version="$(dpkg-deb --field "${package}" Version)"
package_architecture="$(dpkg-deb --field "${package}" Architecture)"

if [[ "${package_name}" != "kubling-redfish-provider" ]]; then
  echo "unexpected package name: ${package_name}" >&2
  exit 1
fi
if [[ -z "${package_version}" || "${package_version}" != *-1 ]]; then
  echo "unexpected package version: ${package_version}" >&2
  exit 1
fi
if [[ "${package_architecture}" != "amd64" && "${package_architecture}" != "arm64" ]]; then
  echo "unexpected package architecture: ${package_architecture}" >&2
  exit 1
fi

source_version="${package_version%-1}"
expected_name="${package_name}_${source_version}_${package_architecture}.deb"
semver_source_version="${source_version/\~/-}"
semver_expected_name="${package_name}_${semver_source_version}_${package_architecture}.deb"
if [[ "$(basename "${package}")" != "${expected_name}" && \
      "$(basename "${package}")" != "${semver_expected_name}" ]]; then
  echo "unexpected package filename: $(basename "${package}")" >&2
  exit 1
fi

extraction_directory="$(mktemp -d)"
trap 'rm -rf -- "${extraction_directory}"' EXIT
dpkg-deb --extract "${package}" "${extraction_directory}"

test -x "${extraction_directory}/usr/bin/kubling-redfish-provider"
test -f "${extraction_directory}/etc/kubling/redfish-provider.yaml.example"
test -f "${extraction_directory}/usr/share/doc/kubling-redfish-provider/README.md"
test -f "${extraction_directory}/usr/share/doc/kubling-redfish-provider/copyright"

printf '%s %s %s\n' \
  "${package_name}" "${package_version}" "${package_architecture}"
