<?php
declare(strict_types=1);

// Started only by TestWHMCSPHPRealHandlerContract. No external endpoint allowed.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('CLI fixture only'); }
define('BAREPULSE_ALLOW_LOOPBACK_TESTS', true);
require dirname(__DIR__) . '/barepulse/lib/ApiClient.php';

try {
    $config = json_decode(stream_get_contents(STDIN), true, 32, JSON_THROW_ON_ERROR);
    $origin = parse_url((string) ($config['origin'] ?? ''));
    if (!is_array($origin) || ($origin['scheme'] ?? '') !== 'http'
        || !in_array($origin['host'] ?? '', ['127.0.0.1', '[::1]'], true)) {
        throw new RuntimeException('Platform contract requires a literal loopback HTTP fixture.');
    }
    $api = new \BarePulse\ApiClient($config['origin'], $config['instance'], $config['secret']);
    $assertions = 0;
    $check = static function (bool $condition, string $message) use (&$assertions): void {
        ++$assertions;
        if (!$condition) throw new RuntimeException($message);
    };
    $expectError = static function (callable $call, string $code, int $status) use ($check): void {
        try { $call(); }
        catch (\BarePulse\ApiException $error) {
            $check($error->errorCode === $code && $error->httpStatus === $status, 'Unexpected platform rejection contract');
            return;
        }
        throw new RuntimeException('Platform accepted an unauthorized or conflicting request.');
    };
    $service = $config['service_id'];
    $client = ['client_id' => $config['client_id']];
    $summary = $api->request('GET', $service, 'summary', $client);
    $check($summary['service_id'] === $service && $summary['state'] === 'active', 'Service identity/state mismatch');
    $check($summary['server']['id'] === $config['server_id'], 'Wrong mapped server');
    $power = $api->request('GET', $service, 'power', $client);
    $check($power['simulated'] === true && $power['execution_enabled'] === true, 'Mock execution capability mismatch');
    $check(in_array('power_on', $power['actions'], true) && !in_array('disable_port', $power['actions'], true), 'Customer action scope mismatch');
    $traffic = $api->request('GET', $service, 'traffic', $client);
    $check($traffic['account']['id'] === $config['account_id'], 'Wrong traffic account');
    $check($traffic['effective_quota_bytes'] === $config['quota'] && $traffic['account']['quota_bytes'] === $config['quota'], 'Large exact quota lost across PHP/Go JSON');
    $check($traffic['used_bytes'] === '0' && $traffic['coverage_note'] !== '', 'Missing traffic coverage/decimal contract');
    $check($traffic['enforcement_enabled'] === false, 'Unconfirmed enforcement enabled');
    $expectError(static fn() => $api->request('GET', $service, 'summary', ['client_id' => '999']), 'SERVICE_NOT_FOUND', 404);
    $expectError(static fn() => $api->request('GET', '102', 'summary', $client), 'SERVICE_NOT_FOUND', 404);
    $request = $client + ['action' => 'power_on', 'idempotency_key' => 'php-real-go-contract-once'];
    $first = $api->request('POST', $service, 'power-actions', $request);
    $check(is_string($first['id']) && preg_match('/^[a-f0-9]{48}$/D', $first['id']) === 1, 'Task ID must be 48 hex');
    $again = $api->request('POST', $service, 'power-actions', $request);
    $check($again['id'] === $first['id'], 'Power retry created another task');
    $request['action'] = 'hard_reset';
    $expectError(static fn() => $api->request('POST', $service, 'power-actions', $request), 'IDEMPOTENCY_CONFLICT', 409);
    $task = $api->request('GET', $service, 'task', $client + ['task_id' => $first['id']]);
    $check($task['id'] === $first['id'] && $task['server_id'] === $config['server_id'] && $task['status'] === 'queued', 'GET task query or asynchronous state mismatch');
    $console = $api->request('POST', $service, 'console', $client);
    $check($api->validatePortalURL($console['portal_url']) === $console['portal_url'] && $console['expires_in'] === 60, 'Portal URL or TTL mismatch');
    $check(preg_match('/^[a-f0-9]{48}$/D', $console['session']['id']) === 1 && $console['session']['server_id'] === $config['server_id'], 'Console ID/ownership mismatch');
    $check(in_array($console['session']['state'], ['active', 'queued'], true), 'Unexpected initial console state');
    echo json_encode(['assertions' => $assertions, 'task_id' => $first['id'], 'console_id' => $console['session']['id']], JSON_THROW_ON_ERROR), "\n";
} catch (Throwable $error) {
    // Only sanitized ApiClient errors and controlled assertions, never fixture secrets.
    fwrite(STDERR, 'FAIL: ' . $error->getMessage() . "\n");
    exit(1);
}
