# Releasing a plugin

Each plugin is versioned independently.

1. Ensure `go test -race ./...`, `go vet ./...`, all Linux static builds, and
   `go run ./tools/pluginregistry validate` pass on `main`.
2. Create an annotated SemVer tag named `<plugin>-vX.Y.Z`, for example
   `docker-base-v1.0.0`.
3. Push the tag. The protected release workflow validates the tag, rebuilds the
   selected binary with `CGO_ENABLED=0`, and injects version, commit, and build
   date with ldflags. It first attempts to create a draft with all assets in one
   command. After a runner crash or partial upload, a retry resumes only an
   unpublished draft whose tag and target commit match exactly. Existing assets
   are downloaded and compared byte-for-byte, unexpected or mismatched assets
   fail closed, and only missing assets are uploaded without clobbering. Every
   asset is downloaded and verified again before the draft is published. A
   retry against an already-published release is a successful immutable no-op
   only when the tag commit and complete asset set match the staged bytes
   exactly. An incomplete release, unexpected asset, byte mismatch, or target
   mismatch fails closed without modifying the published release.
   Build and publication timestamps come from the immutable tag commit, and the
   SPDX creation time and namespace are normalized deterministically, so a retry
   rebuilds the same bytes.
4. Verify the raw `linux/amd64` binary, SHA-256 file, SPDX JSON SBOM, exact
   manifest, and `<plugin>_release-metadata-v1.json`.
5. Import the sidecar with the catalog tooling. It binds the immutable URL,
   size, SHA-256, manifest, minimum host version, publication time, and
   description to the exact release bytes.
6. Publish a signed catalog snapshot only after CI and maintainer approval.

Do not replace an existing release asset or reuse a tag. Correct a release by
publishing a new plugin version.
