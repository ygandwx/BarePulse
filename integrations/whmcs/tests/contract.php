<?php
declare(strict_types=1);

namespace WHMCS\Authentication {
    final class CurrentUser {
        public static ?string $clientId = '7';
        public function client() { return self::$clientId === null ? null : (object) ['id' => self::$clientId]; }
        public function isAuthenticatedUser(): bool { return self::$clientId !== null; }
        public function isMasqueradingAdmin(): bool { return false; }
    }
}
namespace WHMCS\Database {
    final class Capsule {
        public static bool $exists = true;
        private array $where = [];
        public static function table(string $name): self { if ($name !== 'tblhosting') throw new \RuntimeException('wrong ownership table'); return new self(); }
        public function where(string $column, string $value): self { $this->where[$column] = $value; return $this; }
        public function exists(): bool { return self::$exists && $this->where === ['id' => '42', 'userid' => '7']; }
    }
}
namespace {
define('WHMCS', true);
define('BAREPULSE_ALLOW_LOOPBACK_TESTS', true);
require __DIR__ . '/../barepulse/barepulse.php';

$checks = 0;
function check(bool $condition, string $message): void { global $checks; ++$checks; if (!$condition) throw new RuntimeException($message); }
function expectError(callable $fn, string $code): void {
    try { $fn(); throw new RuntimeException('Expected error ' . $code); }
    catch (\BarePulse\ApiException $e) { check($e->errorCode === $code, 'Unexpected error: ' . $e->errorCode); }
}

final class FakeAPI implements \BarePulse\ServiceAPI {
    public array $calls = [];
    public bool $timeout = false;
    public function request(string $method, string $serviceId, string $action, array $data): array {
        $this->calls[] = [$method, $serviceId, $action, $data];
        check($serviceId === '42' && ($data['client_id'] ?? null) === '7', 'Browser identity reached API');
        if ($action === 'power-actions') {
            if ($this->timeout) throw new \BarePulse\ApiException('PLATFORM_UNAVAILABLE', 'fixture timeout');
            return ['id' => str_repeat('a', 48), 'status' => 'queued'];
        }
        if ($action === 'summary') return ['state' => 'active', 'server' => ['id' => 'server-fixture', 'name' => '<script>fixture</script>', 'model' => 'test']];
        if ($action === 'power') return ['power_state' => 'unknown', 'supply_state' => 'on', 'quality' => 'cached', 'simulated' => true, 'execution_enabled' => true, 'actions' => ['power_on', 'disable_port', 'hard_reset']];
        if ($action === 'traffic') return ['upload_bytes' => '18446744073709551615', 'download_bytes' => '2', 'coverage_note' => 'confirmed intervals only'];
        if ($action === 'task') return ['id' => str_repeat('a', 48), 'status' => 'unknown', 'action' => 'hard_reset', 'result' => ['accepted' => true, 'completed' => false, 'target_reached' => false]];
        if ($action === 'console') return ['portal_url' => 'https://platform.example.invalid/portal.html#' . str_repeat('c', 48)];
        throw new RuntimeException('Unexpected API action ' . $action);
    }
    public function writes(): array { return array_values(array_filter($this->calls, static fn($call) => $call[0] === 'POST')); }
}

$secret = str_repeat('offline-fixture-', 3);
$captured = [];
$api = new \BarePulse\ApiClient('https://platform.example.invalid', 'fixture-instance', $secret,
    static function ($method, $url, $headers, $body) use ($secret, &$captured) {
        $h = [];
        foreach ($headers as $line) { [$k, $v] = explode(': ', $line, 2); $h[$k] = $v; }
        $u = parse_url($url); $uri = $u['path'] . (isset($u['query']) ? '?' . $u['query'] : '');
        $canonical = implode("\n", [$method, $uri, $h['X-BarePulse-Timestamp'], $h['X-BarePulse-Nonce'], hash('sha256', $body)]);
        check(hash_equals(hash_hmac('sha256', $canonical, $secret), $h['X-BarePulse-Signature']), 'HMAC mismatch');
        check(strlen($h['X-BarePulse-Nonce']) >= 24, 'Nonce too short');
        $captured[] = [$method, $uri, $body, $h];
        return ['status' => 200, 'body' => '{"ok":true}'];
    });
$api->request('GET', '42', 'task', ['client_id' => '7', 'task_id' => str_repeat('a', 48)]);
$api->request('POST', '42', 'power-actions', ['client_id' => '7', 'action' => 'power_on', 'idempotency_key' => 'fixture-key']);
check($captured[0][1] === '/api/v1/whmcs/services/42/task?client_id=7&task_id=' . str_repeat('a', 48), 'GET canonical query mismatch');
check($captured[0][2] === '' && str_contains($captured[1][2], '"client_id":"7"'), 'Raw body or string ID mismatch');
check($captured[0][3]['X-BarePulse-Nonce'] !== $captured[1][3]['X-BarePulse-Nonce'], 'Nonce reused');
expectError(static fn() => $api->request('POST', '../1', 'console', []), 'REQUEST_INVALID');
expectError(static fn() => $api->validatePortalURL('https://evil.invalid/portal.html#' . str_repeat('a', 48)), 'PORTAL_INVALID');
foreach (['http://platform.example.invalid', 'https://u:p@platform.example.invalid', 'https://platform.example.invalid/api', 'https://platform.example.invalid?x=1'] as $url) {
    expectError(static fn() => new \BarePulse\ApiClient($url, 'fixture', $secret), 'CONFIG_INVALID');
}
$errorAPI = new \BarePulse\ApiClient('https://platform.example.invalid', 'fixture', $secret, static fn() => ['status' => 409, 'body' => '{"error":{"code":"PROTECTED_SESSION","message":"Installation protected"}}']);
expectError(static fn() => $errorAPI->request('POST', '42', 'power-actions', ['client_id' => '7']), 'PROTECTED_SESSION');
$badAPI = new \BarePulse\ApiClient('https://platform.example.invalid', 'fixture', $secret, static fn() => ['status' => 200, 'body' => '<html>error</html>']);
expectError(static fn() => $badAPI->request('GET', '42', 'power', []), 'RESPONSE_INVALID');

$fake = new FakeAPI(); $session = [];
$area = new \BarePulse\ClientArea($fake, [$api, 'validatePortalURL']);
$view = $area->render('42', '7', 'GET', [], $session);
check($view['traffic']['upload_bytes'] === '18446744073709551615', 'Traffic integer precision lost');
check(count($view['actions']) === 2, 'Maintenance action leaked to customer controls');
$post = ['bp_csrf' => $view['csrf'], 'bp_request_id' => $view['request_id'], 'bp_action' => 'hard_reset', 'client_id' => '999', 'server_id' => 'other', 'port' => '16'];
$view = $area->render('42', '7', 'POST', $post, $session);
check(count($fake->writes()) === 1 && !$view['task']['completed'] && $view['task']['accepted'], 'Accepted/reset outcome misreported');
$area->render('42', '7', 'POST', $post, $session);
check(count($fake->writes()) === 1, 'Double submission issued another API write');
$changed = $post; $changed['bp_action'] = 'power_on';
expectError(static function () use ($area, $changed, &$session) { $area->render('42', '7', 'POST', $changed, $session); }, 'IDEMPOTENCY_CONFLICT');
$bad = $post; $bad['bp_csrf'] = 'forged'; $callCount = count($fake->calls);
expectError(static function () use ($area, $bad, &$session) { $area->render('42', '7', 'POST', $bad, $session); }, 'CSRF_INVALID');
check(count($fake->calls) === $callCount, 'Invalid CSRF contacted platform');

$timeoutAPI = new FakeAPI(); $timeoutAPI->timeout = true; $timeoutSession = [];
$timeoutArea = new \BarePulse\ClientArea($timeoutAPI, [$api, 'validatePortalURL']);
$first = $timeoutArea->render('42', '7', 'GET', [], $timeoutSession);
$retry = ['bp_csrf' => $first['csrf'], 'bp_request_id' => $first['request_id'], 'bp_action' => 'power_on'];
$errorView = $timeoutArea->render('42', '7', 'POST', $retry, $timeoutSession);
check($errorView['request_id'] === $first['request_id'] && str_contains($errorView['error'], 'PLATFORM_UNAVAILABLE'), 'Timeout lost idempotency key or error');
$timeoutAPI->timeout = false; $timeoutArea->render('42', '7', 'POST', $retry, $timeoutSession);
$writes = $timeoutAPI->writes();
check(count($writes) === 2 && $writes[0][3]['idempotency_key'] === $writes[1][3]['idempotency_key'], 'Manual retry created new idempotency key');

$params = ['serviceid' => '42', 'userid' => '7'];
check(barepulse_serviceIdentity($params, true) === ['42', '7'], 'Owner verification failed');
\WHMCS\Authentication\CurrentUser::$clientId = '8';
expectError(static fn() => barepulse_serviceIdentity($params, true), 'FORBIDDEN');
\WHMCS\Authentication\CurrentUser::$clientId = null;
expectError(static fn() => barepulse_serviceIdentity($params, true), 'FORBIDDEN');
\WHMCS\Authentication\CurrentUser::$clientId = '7'; \WHMCS\Database\Capsule::$exists = false;
expectError(static fn() => barepulse_serviceIdentity($params, true), 'FORBIDDEN');
\WHMCS\Database\Capsule::$exists = true;
check(barepulse_ClientAreaCustomButtonArray() === [], 'Unguarded custom action buttons exposed');
$template = file_get_contents(__DIR__ . '/../barepulse/clientarea.tpl');
check(str_contains($template, "|escape:'html'") && !str_contains($template, 'configoption3') && !str_contains($template, 'Signature'), 'Template exposes credentials or omits escaping');
echo 'PASS: ' . $checks . " offline WHMCS assertions (framework stubs; no live WHMCS).\n";
}
