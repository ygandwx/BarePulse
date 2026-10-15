# WHMCS module implementation and acceptance

The [module source and installation guide](../../integrations/whmcs/README.md)
implement a provisioning module, customer product-detail template, signed
server-to-server API client, access lifecycle functions, session CSRF protection,
power submission idempotency, task polling and one-use console portal links.

The platform independently validates integration/service/client/server ownership
and rejects replayed HMAC nonces. The module adds an active `CurrentUser` client
check and a `tblhosting` ownership query before any client-area API request.
Neither browser-supplied `client_id`, server ID, port, credentials nor arbitrary
device URLs are trusted. The normal WHMCS product-details route remains the entry
point, so installed WHMCS account/contact permissions also apply.

Secrets reside in server environment variables referenced by module configuration.
The custom template receives only a filtered view model, a session CSRF token,
form idempotency key and an explicitly requested short-lived portal ticket. It
does not receive the integration secret. All dynamic template text and attributes
are escaped. API transport verifies HTTPS, refuses redirects, bounds response
size and time, and never automatically resends POST. Errors stay visible.

Create verifies an administrator's existing mapping before returning WHMCS
`success`. Suspend/resume alter access only. Terminate surfaces `SERVICE_BUSY`
while protected sessions or uncertain/running operations remain. Payment state is
never translated into a physical power/network operation. Task display reports
acceptance, completion and target observation separately; traffic bytes remain
decimal strings, with coverage notes and unallocated intervals shown.

## Executed offline evidence

On 2026-10-15, PHP 8.5.11 Windows NTS was obtained from the official PHP download
and checked against SHA-256
`0ea96e0d2b9b737a6036f05cf4e95c49313faa6d0f27bd97edb2742503f0c043`.
This local executable does not alter the system PHP configuration.

| Check | Result |
| --- | --- |
| `php -l` for module and library PHP files | PASS |
| `php integrations/whmcs/tests/contract.php` | PASS: 54 assertions against WHMCS framework/API stubs |
| `php integrations/whmcs/tests/http_contract.php` with cURL enabled | PASS: 69 total assertions, including real loopback HTTP, HMAC, idempotency, lifecycle, owned client page and console contracts |
| Live licensed WHMCS, Smarty rendering and installed theme integration | NOT_RUN: no WHMCS test instance supplied |
| Actual customer device or power requests | NOT_RUN: no device authorization; tests use isolated fixtures |

Offline checks cover exact raw-body/query signing, fresh nonces, numeric string
identifiers, ownership mismatch, unauthenticated client, missing database ownership,
CSRF rejection before network I/O, hidden maintenance actions, duplicate submission,
idempotency-key retention after timeout, partial/unknown task semantics, large
traffic integers, malformed API responses, disabled redirect following, lifecycle
conflicts and no secret in the template variables. Fixture routes are not deployed.

## Required licensed-instance checks

- Install on the actual supported WHMCS/PHP combination and test the selected
  Smarty theme; confirm normal product-page account/contact permission checks.
- Log in as two client accounts. Tamper with service IDs and submitted fields;
  confirm the other account's server, traffic, task and console remain inaccessible.
- Submit repeated POSTs, an expired CSRF token and a response-loss fault. Verify
  stable task identity and that neither the browser nor module repeats a device
  command. Confirm task refresh uses GET rather than form resubmission.
- Verify the configured PHP-FPM environment, CA trust and system clock. Confirm
  platform errors appear without credentials in page HTML, logs or browser storage.
- Suspend, resume and terminate a mapped service while queued tasks, protected
  installation work and unknown outcomes exist. Confirm access revocation and
  explicit conflicts without physical cutoff or loss of protection.
- Redeem a console ticket once, then test replay and expiry. Verify its actual
  media revocation separately under the console/device acceptance procedure.

References: [WHMCS Client Area Output](https://developers.whmcs.com/provisioning-modules/client-area-output),
[WHMCS Core Module Functions](https://developers.whmcs.com/provisioning-modules/core-module-functions),
[WHMCS authentication via CurrentUser](https://developers.whmcs.com/advanced/authentication).
These document module entry points; they do not replace installed-version testing.
