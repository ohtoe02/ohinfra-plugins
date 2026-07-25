# Protocol v1 contract bundle

This directory is the canonical, language-neutral contract bundle for
executable ohtools plugins. It does not define a public Go SDK.

JSON Schema captures the wire shape. The conformance vectors capture semantic
rules that JSON Schema cannot express, including command ownership, `use`/path
agreement, argument ordering, host-reserved flags, UTF-8 byte limits, typed
defaults, strict Invocation decoding, plan digest bytes, and exit mapping.

Help text is limited to 512 UTF-8 bytes, not 512 Unicode code points. The
`x-ohtools-max-utf8-bytes` schema annotation is normative and is exercised by
`manifest-semantics.json`.

Plan digest v1 intentionally preserves the bytes produced by the existing Go
host and is therefore not RFC 8785. `ohtools-plan-json-v1` means:

- emit Plan fields in this order: `command_id`, `summary`, `checks`, `changes`,
  `risks`, `requires_root`, `requires_force`, `requires_confirmation`; Check
  fields use `id`, `status`, `summary`, optional `details`, and Change fields
  use `object`, `action`, `status`, optional `details`; emit no insignificant
  whitespace or trailing newline;
- normalize absent `checks`, `changes`, and `risks` to empty arrays;
- omit absent or empty optional `details` objects;
- recursively sort keys of free-form JSON objects by Unicode scalar value;
- encode finite IEEE-754 binary64 numbers with the shortest decimal that
  round-trips to the same value; use fixed notation for absolute values in
  `[1e-6, 1e21)` and lowercase exponent notation outside that interval,
  removing a leading zero from the exponent; encode positive zero as `0` and
  preserve negative zero as `-0`;
- use UTF-8 JSON strings, escaping quotation mark, reverse solidus, C0
  controls, `<`, `>`, `&`, U+2028, and U+2029 exactly as Go `encoding/json`.

The digest is lowercase SHA-256 over those exact bytes. Changing this
canonicalization requires a negotiated protocol version; the expanded vectors
cover nested keys, numeric values, and Unicode without changing existing v1
digests.

Consumers vendor the complete directory, verify `SHA256SUMS`, and record the
source catalog commit plus the SHA-256 of the `SHA256SUMS` file. The checksum
file deliberately does not include itself.
