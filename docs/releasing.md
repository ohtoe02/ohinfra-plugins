# Releasing a plugin

Each plugin is versioned independently.

1. Ensure `go test -race ./...`, `go vet ./...`, all Linux static builds, and
   `go run ./tools/pluginregistry validate` pass on `main`.
2. Create an annotated SemVer tag named `<plugin>-vX.Y.Z`, for example
   `docker-base-v1.0.0`.
3. Push the tag. The protected release workflow validates the tag, rebuilds the
   selected binary with `CGO_ENABLED=0`, and injects version, commit, and build
   date with ldflags. It atomically creates a new draft release, fails if that
   release already exists, uploads without clobbering assets, and only then
   publishes the draft.
4. Verify the raw `linux/amd64` binary, SHA-256 file, SPDX JSON SBOM, exact
   manifest, and `<plugin>_release-metadata-v1.json`.
5. Import the sidecar with the catalog tooling. It binds the immutable URL,
   size, SHA-256, manifest, minimum host version, publication time, and
   description to the exact release bytes.
6. Publish a signed catalog snapshot only after CI and maintainer approval.

Do not replace an existing release asset or reuse a tag. Correct a release by
publishing a new plugin version.
