<?php
declare(strict_types=1);
// Offline loopback fixture only. Never deploy the tests directory to WHMCS.
if (PHP_SAPI !== 'cli-server' || !in_array($_SERVER['REMOTE_ADDR'] ?? '', ['127.0.0.1', '::1'], true)) { http_response_code(403); exit; }
header('Content-Type: application/json');
function fixtureError(int $status, string $code): void { http_response_code($status); echo json_encode(['error' => ['code' => $code, 'message' => 'Offline fixture: ' . $code]]); exit; }
$statePath = getenv('BAREPULSE_WHMCSTEST_STATE');
$state = json_decode(file_get_contents($statePath), true, 64, JSON_THROW_ON_ERROR);
$method = $_SERVER['REQUEST_METHOD']; $uri = $_SERVER['REQUEST_URI']; $body = file_get_contents('php://input');
$stamp = $_SERVER['HTTP_X_BAREPULSE_TIMESTAMP'] ?? ''; $nonce = $_SERVER['HTTP_X_BAREPULSE_NONCE'] ?? '';
$signature = $_SERVER['HTTP_X_BAREPULSE_SIGNATURE'] ?? '';
$canonical = implode("\n", [$method, $uri, $stamp, $nonce, hash('sha256', $body)]);
if (($_SERVER['HTTP_X_BAREPULSE_INSTANCE'] ?? '') !== 'fixture' || !ctype_digit($stamp) || abs(time() - (int) $stamp) > 60
    || strlen($nonce) < 24 || !hash_equals(hash_hmac('sha256', $canonical, getenv('BAREPULSE_WHMCSTEST_SECRET')), $signature)) fixtureError(401, 'INTEGRATION_AUTH');
if (isset($state['nonces'][$nonce])) fixtureError(401, 'NONCE_REPLAY');
$state['nonces'][$nonce] = true;
file_put_contents($statePath, json_encode($state, JSON_THROW_ON_ERROR));
$data = $method === 'GET' ? $_GET : json_decode($body, true, 32, JSON_THROW_ON_ERROR);
if (!preg_match('~^/api/v1/whmcs/services/42/(summary|power|traffic|task|power-actions|console|lifecycle)(?:\?|$)~', $uri, $match) || ($data['client_id'] ?? null) !== '7') fixtureError(404, 'SERVICE_NOT_FOUND');
$action = $match[1];
if ($state['mode'] === 'redirect') { header('Location: http://127.0.0.1:1/', true, 307); echo '{}'; exit; }
if ($state['mode'] === 'malformed') { echo '<html>fixture</html>'; exit; }
switch ($action) {
case 'summary': echo json_encode(['state' => 'active', 'server' => ['id' => 'fixture', 'name' => 'Fixture server']]); break;
case 'power': echo json_encode(['power_state' => 'unknown', 'supply_state' => 'on', 'actions' => ['power_on'], 'simulated' => true]); break;
case 'traffic': echo json_encode(['upload_bytes' => '18446744073709551615', 'download_bytes' => '0',
    'intervals' => [['source_id' => 'loopback-source', 'starts_at' => '2026-10-15T00:00:00Z', 'ends_at' => '2026-10-15T00:01:00Z', 'upload_bytes' => '6000', 'download_bytes' => '12000', 'quality' => 'normal', 'billable' => true]],
    'samples' => [['source_id' => 'loopback-source', 'observed_at' => '2026-10-15T00:01:00Z', 'quality' => 'normal', 'error_code' => '']]]); break;
case 'power-actions':
    if ($method !== 'POST' || !is_string($data['idempotency_key'] ?? null)) fixtureError(400, 'IDEMPOTENCY_REQUIRED');
    $key = $data['idempotency_key'];
    if (isset($state['tasks'][$key]) && $state['tasks'][$key]['action'] !== $data['action']) fixtureError(409, 'IDEMPOTENCY_CONFLICT');
    if (!isset($state['tasks'][$key])) { $state['writes']++; $state['tasks'][$key] = ['id' => str_repeat('a', 48), 'status' => 'queued', 'action' => $data['action']]; }
    file_put_contents($statePath, json_encode($state, JSON_THROW_ON_ERROR)); http_response_code(202); echo json_encode($state['tasks'][$key]); break;
case 'task': echo json_encode(['id' => str_repeat('a', 48), 'status' => 'unknown', 'result' => ['accepted' => true, 'completed' => false, 'target_reached' => false]]); break;
case 'console': http_response_code(202); echo json_encode(['portal_url' => 'http://' . $_SERVER['HTTP_HOST'] . '/portal.html#' . str_repeat('b', 48), 'expires_in' => 60, 'session' => ['state' => 'queued']]); break;
case 'lifecycle':
    if (($data['state'] ?? '') === 'terminated' && $state['protected']) fixtureError(409, 'SERVICE_BUSY');
    echo json_encode(['state' => $data['state'], 'physical_policy' => 'disabled']); break;
default: fixtureError(405, 'ACTION_UNSUPPORTED');
}
