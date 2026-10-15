# WHMCS provisioning module

Copy only the `barepulse/` directory to `WHMCS/modules/servers/barepulse/`.
Do not publish the `tests/` directory. No Composer dependency is required.

Prerequisites: a WHMCS installation with the `CurrentUser` API introduced in
WHMCS 8, PHP with cURL and JSON, and a valid HTTPS origin for BarePulse. PHP source
uses PHP 8 syntax. Verify the exact WHMCS/PHP/theme versions in a licensed test
installation before enabling customer access; a real WHMCS installation was not
available for this implementation.

## Setup

1. In BarePulse, create an integration with a `BAREPULSE_...` environment secret
   reference. Supply the same randomly generated secret, at least 32 bytes, to
   the BarePulse server and the WHMCS PHP worker environment. Keep it outside the
   web root and prevent process/environment dumps in diagnostics.
2. On the WHMCS product's module settings, select BarePulse. Configure the
   platform origin (`https://barepulse.example.invalid`), integration ID, and
   **environment variable name**, such as `BAREPULSE_WHMCSPROD_SECRET`.
   Do not paste the actual secret into module configuration or custom fields.
   If PHP-FPM clears worker environment variables, explicitly allow this one
   configured variable in the pool's deployment configuration.
3. In BarePulse, pre-bind the WHMCS service ID and client account ID to an existing
   physical server owned by the corresponding platform customer. Identifiers
   must match on both systems. `CreateAccount` verifies this mapping; it never
   provisions or assigns physical hardware automatically.
4. Visit the owned WHMCS product details page. It displays power/supply separately,
   traffic totals and quality, tasks, allowed power actions and a shared-console
   request. Its console link is a one-use, 60-second portal ticket. Long-term
   integration or device credentials are never sent to the browser.
5. Exercise the deployment acceptance cases in [the module notes](../../docs/modules/whmcs.md)
   against an isolated WHMCS test installation before rollout.

The module does not register bare custom-action buttons: actions use its own POST
form on the ordinary product details page, with both WHMCS's template token and a
module session CSRF token. It independently validates the active client account
against the service parameters and `tblhosting`. WHMCS's normal product-page
access rules remain in force. The server derives both service and client IDs;
browser fields cannot override these or select device ports.

`SuspendAccount` and `UnsuspendAccount` change platform access only. There is no
automatic billing, throttle, network cutoff or power cutoff. `TerminateAccount`
returns a visible conflict while protected console work or uncertain/running tasks
need reconciliation. It does not erase disks or silently release protection.

## Offline checks

```sh
php integrations/whmcs/tests/contract.php
php integrations/whmcs/tests/http_contract.php
```

The second command requires PHP cURL and starts its own loopback-only PHP fixture,
then terminates it and removes only its own temporary fixture files. It does not
call a device or a real WHMCS service. The `BAREPULSE_ALLOW_LOOPBACK_TESTS` constant
is defined only by the test harness; production requires HTTPS and verifies TLS.

For the downloaded Windows CLI in this development workspace:

```powershell
& ../toolchain/php/php.exe -d extension_dir=../toolchain/php/ext -d extension=curl integrations/whmcs/tests/http_contract.php
```

PHP 8.5.11 NTS was downloaded from the official PHP distribution and its SHA-256
verified before use. This verifies the standalone module code and HTTP contracts;
it is not a claim that a specific installed WHMCS release supports PHP 8.5.

## Request signing

The canonical HMAC-SHA256 input is `METHOD`, request URI including the exact query,
Unix seconds, nonce and lowercase hex SHA-256 of the raw body, separated by single
LF characters. The signature is lowercase hex. GET has an empty body. Requests use
fresh random 48-character hex nonces and `X-BarePulse-Instance`,
`X-BarePulse-Timestamp`, `X-BarePulse-Nonce`, `X-BarePulse-Signature` headers.
Both service and client IDs are decimal **strings**.

Each power form has an idempotency key retained across a lost reply. Duplicate
confirmed submissions return the same task locally; manual retries after a lost
reply use the same platform key. HTTP transport never retries automatically or
follows redirects. The task UI distinguishes accepted, completed and target reached;
unknown means support must reconcile the existing task. A later ON value cannot
prove a hardware reset completed.
