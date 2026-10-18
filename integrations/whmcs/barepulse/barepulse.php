<?php
declare(strict_types=1);

if (!defined('WHMCS')) { http_response_code(403); exit('Direct access is not allowed.'); }

require_once __DIR__ . '/lib/ApiClient.php';
require_once __DIR__ . '/lib/ClientArea.php';

function barepulse_MetaData(): array
{
    return ['DisplayName' => 'BarePulse', 'APIVersion' => '1.1', 'RequiresServer' => false];
}

function barepulse_ConfigOptions(): array
{
    return [
        'Platform origin' => ['Type' => 'text', 'Size' => '60', 'Description' => 'HTTPS origin, for example https://barepulse.example.invalid'],
        'Integration ID' => ['Type' => 'text', 'Size' => '40', 'Description' => 'Integration ID created by a BarePulse administrator'],
        'Secret environment variable' => ['Type' => 'text', 'Size' => '50', 'Description' => 'Environment variable name only; never paste the secret here'],
    ];
}

function barepulse_api(array $params): \BarePulse\ApiClient
{
    $ref = (string) ($params['configoption3'] ?? '');
    if (!preg_match('/^BAREPULSE_[A-Z0-9_]{3,100}$/D', $ref)) {
        throw new \BarePulse\ApiException('CONFIG_INVALID', 'Configure a BAREPULSE_ secret environment variable reference.');
    }
    return new \BarePulse\ApiClient((string) ($params['configoption1'] ?? ''), (string) ($params['configoption2'] ?? ''), (string) getenv($ref));
}

function barepulse_serviceIdentity(array $params, bool $clientArea): array
{
    $service = (string) ($params['serviceid'] ?? '');
    $owner = (string) ($params['userid'] ?? '');
    if (!preg_match('/^[1-9][0-9]{0,17}$/D', $service) || !preg_match('/^[1-9][0-9]{0,17}$/D', $owner)) {
        throw new \BarePulse\ApiException('SERVICE_INVALID', 'WHMCS did not provide a valid service owner.');
    }
    if ($clientArea) {
        $current = new \WHMCS\Authentication\CurrentUser();
        $client = $current->client();
        if (!$client || (!$current->isAuthenticatedUser() && !$current->isMasqueradingAdmin()) || (string) $client->id !== $owner) {
            throw new \BarePulse\ApiException('FORBIDDEN', 'This service does not belong to your active client account.');
        }
    }
    if (!\WHMCS\Database\Capsule::table('tblhosting')->where('id', $service)->where('userid', $owner)->exists()) {
        throw new \BarePulse\ApiException('FORBIDDEN', 'Service ownership could not be verified.');
    }
    return [$service, $owner];
}

function barepulse_ClientArea(array $params): array
{
    try {
        [$service, $owner] = barepulse_serviceIdentity($params, true);
        $api = barepulse_api($params);
        $area = new \BarePulse\ClientArea($api, [$api, 'validatePortalURL']);
        $view = $area->render($service, $owner, (string) ($_SERVER['REQUEST_METHOD'] ?? 'GET'), $_POST, $_SESSION);
        $view['authorized'] = true;
    } catch (\BarePulse\ApiException $e) {
        $view = ['authorized' => false, 'error' => $e->errorCode . ': ' . $e->getMessage()];
    } catch (\Throwable $e) {
        // Do not expose framework exception traces or configured secret values.
        $view = ['authorized' => false, 'error' => 'MODULE_UNAVAILABLE: The service view is temporarily unavailable. Contact support.'];
    }
    return ['templatefile' => 'clientarea', 'vars' => ['bp' => $view]];
}

function barepulse_CreateAccount(array $params): string
{
    try {
        [$service, $owner] = barepulse_serviceIdentity($params, false);
        $response = barepulse_api($params)->request('GET', $service, 'summary', ['client_id' => $owner]);
        if (($response['state'] ?? '') !== 'active' || empty($response['server']['id'])) {
            return 'A BarePulse administrator must pre-bind this WHMCS service and owner to an existing server.';
        }
        return 'success';
    } catch (\BarePulse\ApiException $e) {
        return $e->errorCode . ': ' . $e->getMessage() . ' Pre-bind the existing physical server in BarePulse; hardware is not created automatically.';
    } catch (\Throwable $e) { return 'MODULE_UNAVAILABLE: Could not verify the pre-bound physical server.'; }
}

function barepulse_lifecycle(array $params, string $state): string
{
    try {
        [$service, $owner] = barepulse_serviceIdentity($params, false);
        $response = barepulse_api($params)->request('POST', $service, 'lifecycle', ['client_id' => $owner, 'state' => $state]);
        if (($response['state'] ?? '') !== $state || ($response['physical_policy'] ?? '') !== 'disabled') {
            return 'LIFECYCLE_UNVERIFIED: The platform did not confirm the requested access state.';
        }
        return 'success';
    } catch (\BarePulse\ApiException $e) { return $e->errorCode . ': ' . $e->getMessage(); }
    catch (\Throwable $e) { return 'MODULE_UNAVAILABLE: The access state could not be changed.'; }
}
function barepulse_SuspendAccount(array $params): string { return barepulse_lifecycle($params, 'suspended'); }
function barepulse_UnsuspendAccount(array $params): string { return barepulse_lifecycle($params, 'active'); }
function barepulse_TerminateAccount(array $params): string { return barepulse_lifecycle($params, 'terminated'); }

// No raw custom-action buttons: all customer actions pass through the owned
// product page, its POST-only form, CSRF check and durable idempotency key.
function barepulse_ClientAreaCustomButtonArray(): array { return []; }
