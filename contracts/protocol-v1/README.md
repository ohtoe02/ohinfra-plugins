# Protocol v1 contract bundle

This directory is the canonical, language-neutral contract bundle for
executable ohtools plugins. It does not define a public Go SDK.

JSON Schema captures the wire shape. The conformance vectors capture semantic
rules that JSON Schema cannot express, including command ownership, `use`/path
agreement, argument ordering, host-reserved flags, plan digest bytes, and exit
mapping.

Consumers vendor the complete directory, verify `SHA256SUMS`, and record the
source catalog commit plus the SHA-256 of the `SHA256SUMS` file. The checksum
file deliberately does not include itself.
