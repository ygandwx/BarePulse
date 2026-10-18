# SNMPv3 algorithms and offline protocol acceptance

BarePulse traffic collection and APC power transport share explicit algorithm
validation in `drivers.ParseSNMPv3Algorithms` and
`drivers.ValidateSNMPv3Security`. The dependency remains pinned to gosnmp
**v1.45.0**. This implementation does not establish which algorithms or objects
any SN2410, VyOS or APC firmware supports. Real access remains disabled until
separately authorized; the tests below use only `127.0.0.1` UDP listeners.

## Configuration contract

Names are exact and case-sensitive. Missing names, whitespace, aliases and
unknown enum values fail before connecting. There is no default algorithm,
automatic negotiation to weaker settings, community-string fallback, or
fallback from a failed power command to another transport.

| Authentication name | Meaning |
| --- | --- |
| `SHA` | HMAC-SHA-1-96, retained only as an explicit compatibility choice for already supported profiles. |
| `SHA224` | HMAC-SHA-224 truncated to 128 bits. |
| `SHA256` | HMAC-SHA-256 truncated to 192 bits. |
| `SHA384` | HMAC-SHA-384 truncated to 256 bits. |
| `SHA512` | HMAC-SHA-512 truncated to 384 bits. |

The SHA-2 authentication definitions follow
[RFC 7860](https://www.rfc-editor.org/rfc/rfc7860.html#section-4).

| Privacy name | Meaning in the locked library |
| --- | --- |
| `AES` | AES-128-CFB, the RFC 3826 privacy protocol. |
| `AES192` | AES-192-CFB with Blumenthal key extension. Existing power-profile meaning is preserved. |
| `AES256` | AES-256-CFB with Blumenthal key extension. Existing power-profile meaning is preserved. |
| `AES192C` | AES-192-CFB with Reeder key extension, used by Cisco and some other implementations. |
| `AES256C` | AES-256-CFB with Reeder key extension, used by Cisco and some other implementations. |

The enum mapping and extension functions were checked against
[gosnmp v1.45.0 `v3_usm.go`](https://github.com/gosnmp/gosnmp/blob/v1.45.0/v3_usm.go#L130-L140)
and its [key derivation code](https://github.com/gosnmp/gosnmp/blob/v1.45.0/v3_usm.go#L524-L599).
Blumenthal hashes the localized key to extend it; Reeder performs another
password-localization step using that key. The difference matters when the
authentication hash output is shorter than the requested AES key. `AES256` and
`AES256C` must therefore not be treated as interchangeable names.
[RFC 3826](https://www.rfc-editor.org/rfc/rfc3826.html) standardizes AES-128;
the larger variants are extensions, including the
[Blumenthal draft](https://datatracker.ietf.org/doc/html/draft-blumenthal-aes-usm-04),
and require firmware-specific confirmation. A name ending in `C` does not prove
support on a particular Cisco device, much less on APC or Onyx.

`MD5`, `DES`, `NoAuth`, `NoPriv`, SNMPv1 and SNMPv2c are intentionally unavailable
in these adapters. Retaining explicit `SHA` maintains the existing power
configuration contract; it does not become a fallback. Prefer an explicitly
confirmed SHA-2/AES combination for new enrollment. If a device only supports
excluded legacy algorithms, its integration remains blocked until an approved
alternative is available; do not silently change the profile.

USM username length is 1–32 bytes. Both passphrases must be 8–1024 bytes; the upper
bound is a BarePulse resource limit. NUL, CR and LF are rejected. The direct
transport validates numeric enums as well as strings, so invalid enums cannot
reach library crypto routines. Error messages omit passwords, input algorithm
strings, endpoint addresses and raw library responses.

### Traffic credential migration

The environment variable referenced by a real SNMP device's `credential_ref`
now requires **both** algorithm fields. This replaces the previous hardcoded
SHA256/AES pair. Existing credentials missing those fields fail closed and must
be updated deliberately, with actual firmware evidence.

```json
{
  "username": "example-readonly-user",
  "auth_protocol": "SHA256",
  "privacy_protocol": "AES",
  "auth_password": "replace-with-a-unique-auth-passphrase",
  "privacy_password": "replace-with-a-unique-privacy-passphrase",
  "context_name": ""
}
```

This is a schema example, not a usable production credential or device-support
claim. Secret values stay outside Git. The optional context name is forwarded
unchanged. Traffic clients perform only GET/GETBULK; they never SET a port or VLAN.

Power profiles retain `snmp_v3.auth_protocol`, `privacy_protocol`, `auth_password`,
`privacy_password` and `discovery_oid`; the same two explicit `C` variants are
now available. Approval, exact device/server/port/action matching, a confirmed
firmware-specific outlet/OID profile and the separate real-access/write gates
still apply. No real APC OIDs or control enums were added.

## Response and write safety

Initial SNMPv3 engine discovery is the protocol's unauthenticated discovery
exchange. Final data responses must be SNMPv3 USM GetResponse PDUs with both
authentication and privacy bits. The library authenticates and decrypts the
message; BarePulse also checks the final security envelope so an authenticated
cleartext response is rejected. A Reportable flag does not change the security
level. Counter/OID/type validation is applied after envelope validation.

Interface discovery uses explicit bounded GETBULK pages so every response can be
checked, rather than a callback that hides message flags. It rejects
non-increasing ifIndexes, unavailable objects and malformed values, permits at
most 10,000 interfaces, and has an overall deadline. It does not overwrite an
existing source's pinned interface binding. Counter64 values remain decimal
strings through collection, including values beyond 2^53.

Power SET performs a confirmed read-only state GET before installing a one-write
connection fence. `Retries: 0` alone is insufficient because gosnmp can recover
from SNMPv3 time-window or engine REPORTs. After the preflight, only one datagram
can be sent, including through such recovery paths. The final acknowledgement
must match the requested OID, integer type and value with no agent error. A
possibly sent SET without verified acknowledgement remains uncertain, and the
application's durable unknown-task fence must prevent replay. Neither a PDU
acknowledgement nor an outlet ON state establishes server boot completion.

## Tests and evidence

The tests use real UDP sockets, engine discovery REPORTs and encoded USM
authentication/encryption. The fixture uses gosnmp's packet codec; a separate
standard-library HMAC check validates outgoing authenticated datagrams, and
independent password-localization calculations check both key-extension paths.
These are offline protocol tests, not independent vendor interoperability or
physical device acceptance.

| Test | Coverage |
| --- | --- |
| `TestSNMPv3UDPAlgorithmsReadWrite` | All 25 approved combinations; actual discovery, encrypted GET and preflight/SET; exact exchange counts, wire HMAC and scoped-PDU privacy. |
| `TestSNMPv3UDPWriteFailuresNeverResend` | Lost response, modified authentication, wrong username, time/engine REPORT, wrong acknowledgement OID/type/value, agent error and privacy downgrade; at most one SET, and zero SET after failed preflight. |
| `TestSNMPv3AESKeyExtensionDistinct` | Independent localized-key calculation across all combinations; distinguishes SHA-1 with Blumenthal/Reeder extension. |
| `TestSNMPv3RejectInvalidAlgorithmsBeforeNetwork` | Every out-of-policy uint8 enum, omitted/unknown names and excluded legacy algorithms; listener receives no datagram. |
| `TestSNMPv3CredentialBoundsAndResponseSecurity` | Credential bounds and final response version/security model/PDU/auth/privacy requirements. |
| `TestSNMPConfigExplicitAlgorithmsAndPreNetworkErrors` | Same selections in traffic and power; disabled/missing/invalid config rejected without packets or secret-bearing errors. |
| `TestSNMPTrafficUDPCollectAndDiscover` | Actual SHA224/AES256C collection, context forwarding, exact large counters, multi-page discovery, cleartext/type/non-increasing/unavailable-object rejection. |

Executed on 2026-10-19 with the repository's local Go toolchain:

| Command | Actual result |
| --- | --- |
| `go test ./backend/internal/drivers -run '^TestSNMP' -count=1` | PASS: 2.775s. |
| `go test ./backend/internal/app -run '^(TestSNMP\|TestRuntimePower\|TestTraffic)' -count=1`, with the local `TEST_DATABASE_URL` | PASS: 15.166s, including actual PostgreSQL traffic/accounting and power approval regression fixtures. |
| `go vet ./backend/internal/drivers ./backend/internal/app` | PASS, exit 0. |

An initial configuration assertion incorrectly required the complete message
flags value to equal AuthPriv; gosnmp also sets Reportable. It was corrected to
test the authentication/privacy bits, matching the wire-level checks, and the
suite was rerun successfully. No failed security assertion was removed.

Real SN2410/VyOS/APC discovery, firmware algorithm availability, actual counters
and billing direction, real MIB/outlet mappings, management-network packet loss
and physical power outcome are **NOT_RUN**. They need separately scoped device
authorization and evidence. Windows race remains unavailable without CGO; Linux
CI validated these changes in [run 39150257321](https://github.com/ygandwx/BarePulse/actions/runs/39150257321): Linux full-suite race passed (app 18.492s, drivers 5.375s). Remote `6a89172` has the same tree as local `93e1b4b`; this is still offline loopback/PostgreSQL acceptance, not device interoperability.
