# Vendored DMTF Redfish schemas

This directory contains the minimal schema subset used to generate the
`inventory-core` catalog. The files are byte-for-byte copies from the official
[DMTF Redfish-Publications](https://github.com/DMTF/Redfish-Publications)
release `2026.2`, commit `4f81814e399213c9055863ddaff42791c6b3706a`.

`../upstream.lock.json` records every copied file and its SHA-256 digest. Normal
catalog generation is deliberately offline and fails if a vendored file no
longer matches the lock. Upstream updates are explicit reviewable changes to
the lock, schemas and generated catalog.

The upstream files remain available under DMTF's BSD 3-Clause license, retained
in [`LICENSE.md`](LICENSE.md). Kubling-authored files in this repository remain
under the repository's Apache License 2.0.
