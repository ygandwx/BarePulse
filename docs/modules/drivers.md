# Power driver implementation

The application owns authorization, immutable binding snapshots, durable task
idempotency, whole-device scheduling, protection and cooldown persistence. Drivers
receive only the server-resolved `Target` and perform no automatic fallback to
another driver. Customer action names are `power_on`, `request_shutdown`,
`hard_reset`, and `power_cycle`, restricted by each adapter's supported capability.
`hard_power_off` is an explicitly named hard cut supported by a verified adapter
profile, but is not exposed by the current platform power-action API. It is never
a substitute for graceful shutdown. PiKVM `enable_port` / `disable_port` are
administrator-only maintenance actions.

`Result` independently records `accepted`, `completed`, and `target_reached`.
`Status=unknown` with `ACCEPTED_UNVERIFIED` means a command was acknowledged but
there is insufficient physical completion evidence. The scheduler must fence the
device after an uncertain command, preserve its task, and never requeue the write
after worker exit or timeout. Reading ON after reset is not reboot evidence.

## Network boundary

Every HTTP adapter defaults to no networking, including status reads. Loopback
contract tests explicitly allow literal loopback IPs. Real HTTP access requires
`AllowReal`, an exact enrolled host (including port), HTTPS with normal certificate
verification, and separately `AllowWrite` for commands. Device URLs cannot contain
credentials, query parameters, fragments or a base path. Redirects and environment
proxies are disabled. Authentication is supplied by the server, never forwarded
from customer requests. Device response bodies and transport errors are not
copied into customer result messages.

These flags do not themselves constitute device authorization. The application
registers mocks and a gated real-driver factory. Real power execution additionally
requires both deployment flags, an administrator-owned profile file with explicit
approval evidence and unexpired authorization, and exact matching device ID,
kind, literal-IP endpoint, server, port and action. PiKVM mapping verification,
Redfish SystemPath, or confirmed APC firmware/outlet OIDs are mandatory for the
corresponding driver. Profiles and credentials never enter customer responses.
The worker revalidates this authorization before dispatch. All real access stays
disabled by default; no real device was queried. Real console/media access stays
denied until separate connection-revocation and isolation evidence is available.

## Adapter behavior

| Adapter | Implemented evidence and limits |
| --- | --- |
| Mock | Mutex-protected local port state; success/offline/inner-error/timeout/busy faults; disabled PiKVM ports; PDU supply separated from server state; uncertain reset/cycle result. Simulation state is process-local and is not physical evidence after restart. |
| Custom PiKVM | Fixed vendor routes from the repository's [static firmware analysis](../pikvm-integration.md); decimal port 1–16; outer `ok`, inner `error`, integer exit status and string output validation; HTML/missing fields fail closed; one POST; timeout remains unknown. |
| PiKVM state | Complete 16-row `chassis_type` validation with separate connection/enabled/power state. `MappingVerified` defaults false because row order cannot prove physical port identity. `chassis_status` ignores the constant ON field and rejects any error even with fallback `kvm_status=1`. |
| PiKVM maintenance | Enable/disable acceptance or ambiguous outcome returns at least 30 cooldown seconds; no immediate follow-up status read during that period. Scheduler must persist the device-wide deadline. |
| PiKVM switch | Fixed `kvm_node` call followed by exact selected-port confirmation. Caller must already hold the device lease and verify old video/HID/media revocation. Target confirmation alone does not establish media isolation. |
| Redfish | Administrator discovery of `/redfish/v1/Systems` returns candidates without selecting the first. Explicit `ComputerSystem` binding is mandatory. Supported `ResetType` values come from the bound resource or same-system ActionInfo; action targets cannot escape that system. ON/OFF may establish requested state; reset/cycle remain accepted but unverified. |
| APC | A confirmed model, firmware and evidence record supplies each outlet's exact state/control OIDs and integer enum mappings. No AP7921B or AP8xxx OID is guessed or built in. Only known supply state is exposed; server power stays unknown. |
| SNMPv3 | Explicit AuthPriv credentials/algorithms, enrolled literal IP target, access/write gates, zero configured retries and a single-datagram write fence after read-only discovery prevent SET resends even from library engine resynchronization. Errors after a write become unknown. |
| IPMI | Guarded ipmitool 1.8.19 subprocess with approved SHA256, literal IP, fixed LANplus/cipher 17, one command attempt and bounded credentials. State precheck, uncertain-write fencing and reset/cycle evidence rules match the scheduler. See [IPMI contract and profile](ipmi.md); no implicit fallback from other adapters. |

APC `SNMPV3Transport.DiscoveryOID` must be a confirmed read-only integer state OID
from the same firmware profile. Test fixture OIDs under the synthetic 99999 branch
are solely mock values; they are not an APC mapping. Authentication algorithms
must be deliberately configured for the approved device; there is no insecure
community fallback.

## Offline verification

Run `go test ./backend/internal/drivers` and `go vet ./backend/internal/drivers`.
Tests use `httptest` loopback servers and in-memory SNMP responses. They cover
state persistence within the mock process, layered result semantics, inner HTTP
errors, malformed payloads, invalid ports, missing rows, target fallback, timeout
with exactly one POST, cooldown, default network deny, redirect refusal, Redfish
capability and cross-system target checks, confirmed APC profiles, supply/server
separation and the SNMP write fence. They do not establish physical device
behavior, SNMPv3 firmware interoperability or multi-customer media isolation.

Executed on 2026-10-15 with the repository's local Go toolchain:

| Command | Result |
| --- | --- |
| `go test ./backend/internal/drivers` | PASS (2.032 seconds) |
| `go vet ./backend/internal/drivers` | PASS, exit 0 |
| Windows `go test -race ./backend/internal/drivers` | NOT_RUN: this host lacks the required CGO compiler |
| Linux `go test -race ./...` | PASS in [CI run 38709721860](https://github.com/ygandwx/BarePulse/actions/runs/38709721860), before the IPMI increment; latest verification is recorded in implementation-status |

The integrating task also records these results in `docs/implementation-status.md`.
Live device validation remains blocked by the authorization and firmware evidence
gaps described above.

Protocol references: [DMTF Redfish specification, action capability discovery](https://redfish.dmtf.org/schemas/DSP0266_1.17.0.html),
[DMTF ComputerSystem schema](https://github.com/DMTF/Redfish-Publications/blob/main/openapi/ComputerSystem.v1_0_9.yaml),
[gosnmp v1.45.0 source](https://github.com/gosnmp/gosnmp/tree/v1.45.0).
These describe implementation contracts, not proof of installed device support.
