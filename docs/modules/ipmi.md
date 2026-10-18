# IPMI command adapter

`NewIPMI(IPMIConfig) (*IPMICommandDriver, error)` implements a deliberately small
IPMI chassis-power contract using the reviewed upstream `ipmitool` **1.8.19**.
All access, including status, defaults to disabled. The implementation and
offline tests are complete; no BMC or physical server was contacted, and no
firmware support is inferred from these tests.

## Reviewed command and retry contract

The adapter fixes `-I lanplus -C 17 -R 1 -N 2`, supplies an enrolled literal IP,
and invokes one fixed `chassis power` subcommand. It never invokes a shell or
searches `PATH`. Cipher 17 is explicit, so automatic cipher selection cannot
downgrade the connection. Unsupported cipher/firmware responses fail closed.

The upstream 1.8.19 code, rather than the wording of the retry option alone,
determines the safe argument:

- [`ipmi_main.c`, lines 858–862](https://github.com/ipmitool/ipmitool/blob/IPMITOOL_1_8_19/lib/ipmi_main.c#L858-L862)
  only applies a positive retry value. **`-R 0` is not a no-retry setting.**
- [`lanplus.c`, lines 3193–3196](https://github.com/ipmitool/ipmitool/blob/IPMITOOL_1_8_19/src/plugins/lanplus/lanplus.c#L3193-L3196)
  replaces zero with the default. The
  [send loop, lines 1969–2187](https://github.com/ipmitool/ipmitool/blob/IPMITOOL_1_8_19/src/plugins/lanplus/lanplus.c#L1969-L2187)
  starts at zero, sends once per attempt, and increments the attempt counter.
  Thus `-R 1` permits at most one send of that chassis command payload.
- [`ipmi_chassis_power_control`, lines 198–223](https://github.com/ipmitool/ipmitool/blob/IPMITOOL_1_8_19/lib/ipmi_chassis.c#L198-L223)
  makes one `sendrecv` call and emits its acknowledgement only after a successful
  completion code. BarePulse starts at most one write subprocess.
- [Session establishment, lines 3255–3297](https://github.com/ipmitool/ipmitool/blob/IPMITOOL_1_8_19/src/plugins/lanplus/lanplus.c#L3255-L3297)
  can restart the authentication handshake. This is not a claim that the entire
  invocation sends only one UDP datagram. Discovery, session traffic and status
  reads are distinct from the chassis write.

This is a source-level contract for the reviewed version, not a wire-capture
result. A different release, distribution patch affecting these functions, or
unreviewed binary requires renewed review; changing the configured version does
not bypass the exact 1.8.19 check. See the official
[command manual](https://github.com/ipmitool/ipmitool/blob/IPMITOOL_1_8_19/doc/ipmitool.1.in)
and [acknowledgement values](https://github.com/ipmitool/ipmitool/blob/IPMITOOL_1_8_19/lib/ipmi_strings.c#L1230-L1237).

## Server-owned authorization

`IPMIConfig` is supplied by the platform's approved profile factory, never by a
customer request. The application remains responsible for server ownership,
immutable binding snapshots, action eligibility, durable idempotency and the
whole-device task lease.

| Configuration | Required meaning |
| --- | --- |
| `AllowReal` | Explicit deployment permission; false performs no process or network access. |
| `AllowWrites` | Separate permission for all commands except `status`. |
| `Approved`, `Evidence`, `AuthorizationExpiresAt` | Explicit approval, nonempty evidence and a future expiry, rechecked before dispatch. |
| `DeviceID`, `Address` | Exact device and literal IPv4/IPv6 address. No hostname, URL, embedded port, redirect or DNS lookup. |
| `AllowedActions` | Explicit subset of the five actions below; unknown action names invalidate the profile. The constructor copies the list. |
| `BinaryPath`, `BinarySHA256`, `ClientVersion` | Canonical absolute path, approved 64-hex SHA-256, and exactly `1.8.19`. Version output is checked before the first network command. |
| `UDPPort` | 0 means 623; a nonzero explicit UDP port is allowed. This is separate from logical binding `Target.Port`, which must be 1. |
| `Privilege` | Defaults to `OPERATOR`; explicit `ADMINISTRATOR` is also accepted. No automatic privilege escalation. |
| `Timeout` | Whole operation budget, default 15 seconds, maximum 60 seconds; caller cancellation can shorten it. |

The target must have kind `ipmi`, the same device ID and address, and logical port
1. Username is 1–16 bytes and password is 1–20 bytes, without NUL/CR/LF. Passwords
use `-E` and a private `IPMI_PASSWORD` environment variable, never `-P` or argv.
The [upstream environment handling](https://github.com/ipmitool/ipmitool/blob/IPMITOOL_1_8_19/lib/ipmi_main.c#L639-L665)
prefers `IPMITOOL_PASSWORD`, so the child does not inherit that variable. Child
environment is limited to locale, the intended password and Windows SystemRoot
when required; it excludes PATH, loader overrides and unrelated application
secrets. The `-V` process receives no password or endpoint.

The binary hash is verified before each subprocess. The file must be regular,
nonempty, at most 64 MiB, and reached without symlinks; POSIX group/world-writable
or non-executable files are rejected. Windows paths must already be canonical,
including expansion of an 8.3 directory alias. Administrators must also control
the binary directory, dynamic libraries and installation: a hash cannot prevent
a privileged actor replacing a file between verification and execution. Do not
approve a checksum merely because a package reports the expected version.

The application loads the following nested fields from the external
`BAREPULSE_DEVICE_PROFILES_FILE`. This deliberately disabled example is not a
device authorization; IDs, credentials, binary provenance, reviewed checksum,
evidence and time window must all be supplied by an authorized administrator.
The Docker image installs ipmitool at `/usr/bin/ipmitool`; installation alone
does not enable either runtime access gate. Native deployments supply their own
reviewed executable. The application uses a 15-second operation timeout inside
the durable worker's 20-second deadline.

```json
{
  "example-ipmi-reference": {
    "device_id": "example-device-id",
    "kind": "ipmi",
    "endpoint": "192.0.2.45",
    "approved": false,
    "evidence": "Disabled documentation example; no device authorization",
    "authorization_expires_at": "2020-01-01T00:00:00Z",
    "allowed_server_ids": ["example-server-id"],
    "allowed_ports": [1],
    "allowed_actions": ["power_on", "request_shutdown"],
    "username": "example-user",
    "password": "example-not-a-secret",
    "ipmi_profile": {
      "binary_path": "/usr/bin/ipmitool",
      "binary_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
      "client_version": "1.8.19",
      "udp_port": 623,
      "privilege": "OPERATOR"
    }
  }
}
```

Use the profile object's key as the device's `credential_ref`, with an exact
matching address and BMC binding port 1. The UI receives only approved action
names and task results, never this file, executable path or credential fields.
`TestRuntimeIPMIProfileGuards` verifies malformed scopes/client metadata fail
closed; `TestRuntimeIPMIDurableDispatchAndRevocation` uses PostgreSQL and a fake
transport to verify default deny, cross-customer rejection, queued revocation,
uncertain-result fencing, idempotency and secret-free responses.

## Actions and result semantics

Each action first obtains a strict power-state response. Empty, malformed or
error output stops before the write. A successful exit alone is insufficient:
the expected exact acknowledgement and empty stderr are required. Only one
post-command status observation is made for on/shutdown; there is no polling or
write retry loop.

| Platform action | Fixed subcommand | Evidence and outcome |
| --- | --- | --- |
| `status` | `chassis power status` | Observed chassis ON/OFF; `succeeded`, `completed=true`, `accepted=false`, `target_reached=false`. No operating-system boot claim. |
| `power_on` | `chassis power on` | Already ON is a no-write success. Otherwise acknowledgement establishes acceptance; observed ON establishes the requested chassis state. |
| `request_shutdown` | `chassis power soft` | Already OFF is a no-write success. Otherwise acknowledgement establishes acceptance; observed OFF establishes the chassis state, not proof of clean OS shutdown. Requires actual ACPI/OS support. |
| `hard_reset` | `chassis power reset` | Requires pre-command ON. Acknowledgement yields `unknown`, `accepted=true`, `completed=false`, `target_reached=false`; ON cannot prove reset. |
| `power_cycle` | `chassis power cycle` | Same completion rule as reset; pre-command OFF is rejected. Never translated into a separate off/on sequence. |

For an already reached target, `accepted=false`, `completed=true` and
`target_reached=true` mean no write was necessary. After an acknowledged on/soft
command, failure to observe the requested state remains `unknown` with
`accepted=true`. Once the write subprocess started, timeout, nonzero exit,
unexpected output or stderr yields `unknown`; a start failure is safely
`failed`. Raw output, credentials and underlying process errors are never
included in the customer result. Stdout and stderr each have a 1 MiB cap; excess
data is drained without unlimited allocation and invalidates the result.

The worker must retain its durable uncertain-task fence and must not resubmit
reset/cycle or ambiguous writes after timeout/restart. The adapter does not
perform reconciliation or override installation protection. It does not expose
`off`, raw/OEM commands, boot-device changes, SOL, bridging, alternate ciphers or
automatic fallback to another driver.

## Offline verification

Executed on 2026-10-15 using the local Go toolchain:

| Command | Actual result |
| --- | --- |
| `go test ./backend/internal/drivers -run '^TestIPMI' -count=1 -v` | PASS, package 3.240 seconds. |
| `go test ./backend/internal/drivers -count=1` | PASS, package 3.464 seconds. |
| `go vet ./backend/internal/drivers` | PASS, exit 0. |

`TestIPMIDefaultDenyAndExactApproval`, `TestIPMIConfigurationAndBinaryPin`,
`TestIPMICommandContractAndStateSemantics`,
`TestIPMIAmbiguousWriteNeverRetriesOrLeaksOutput`, and
`TestIPMIVersionPrecheckAndExpiryFence` use an injected in-memory runner and an
approved synthetic file. The documentation-range target `192.0.2.45` is never
contacted. `TestIPMIEnvironmentAndRealSubprocessBoundary` runs the Go test binary
as a controlled helper to verify literal arguments, output limits and deadline
termination; it does not execute ipmitool. The initial test run exposed a Windows
8.3 temporary-directory alias in the fixture; canonicalizing the approved fixture
path fixed it before the successful run above.

Actual firmware compatibility, ACPI behavior, power-cycle/reset completion,
approved production binary provenance and packet-loss wire captures remain
**NOT_RUN / external authorization required**. No physical test result is claimed.
Windows race testing is unavailable on this host because CGO is disabled; Linux
CI evidence must be recorded against the exact commit containing this adapter.
