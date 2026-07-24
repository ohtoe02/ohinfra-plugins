# Releasing a plugin

Each plugin is versioned independently.

1. Ensure `go test -race ./...`, `go vet ./...`, and all Linux static builds
   pass on `main`.
2. Create an annotated SemVer tag named `<plugin>-vX.Y.Z`, for example
   `docker-base-v1.0.0`.
3. Push the tag. The protected release workflow validates the tag, rebuilds the
   selected binary with `CGO_ENABLED=0`, and injects version, commit, and build
   date with ldflags.
4. Verify the raw `linux/amd64` binary, SHA-256 file, and SPDX JSON SBOM.
5. Run the manifest in the catalog sandbox and submit the immutable URL, size,
   digest, manifest, minimum host version, publication time, and identical
   optional description to `ohinfra-plugin-catalog`.
6. Publish a signed catalog snapshot only after CI and maintainer approval.

Do not replace an existing release asset or reuse a tag. Correct a release by
publishing a new plugin version.
