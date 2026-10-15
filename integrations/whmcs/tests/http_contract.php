<?php
declare(strict_types=1);
require __DIR__ . '/contract.php';
if (!extension_loaded('curl')) throw new RuntimeException('Enable the PHP cURL extension for the HTTP contract test.');
$work = __DIR__ . '/work';
if (!is_dir($work)) mkdir($work, 0700, true);
$statePath = $work . '/state-' . bin2hex(random_bytes(8)) . '.json';
$logPath = $work . '/http-' . bin2hex(random_bytes(8)) . '.log';
$initial = ['nonces' => [], 'tasks' => [], 'writes' => 0, 'protected' => true, 'mode' => 'normal'];
file_put_contents($statePath, json_encode($initial, JSON_THROW_ON_ERROR));
putenv('BAREPULSE_WHMCSTEST_STATE=' . $statePath);
putenv('BAREPULSE_WHMCSTEST_SECRET=' . str_repeat('offline-fixture-', 3));
$socket = stream_socket_server('tcp://127.0.0.1:0', $errno, $error);
if (!$socket) throw new RuntimeException('Cannot allocate loopback fixture port');
$address = stream_socket_get_name($socket, false); fclose($socket);
$process = proc_open([PHP_BINARY, '-S', $address, __DIR__ . '/router.php'], [0 => ['pipe', 'r'], 1 => ['file', $logPath, 'a'], 2 => ['file', $logPath, 'a']], $pipes, __DIR__, null, ['bypass_shell' => true, 'create_new_console' => false]);
if (!is_resource($process)) throw new RuntimeException('Cannot start offline fixture');
fclose($pipes[0]);
try {
    $ready = false;
    for ($i = 0; $i < 30; ++$i) {
        $probe = @stream_socket_client('tcp://' . $address, $errno, $error, 0.1);
        if ($probe) { fclose($probe); $ready = true; break; }
        usleep(50000);
    }
    check($ready, 'PHP loopback fixture did not start');
    $origin = 'http://' . $address;
    $real = new \BarePulse\ApiClient($origin, 'fixture', (string) getenv('BAREPULSE_WHMCSTEST_SECRET'));
    $summary = $real->request('GET', '42', 'summary', ['client_id' => '7']);
    check($summary['server']['id'] === 'fixture', 'Actual HTTP summary failed');
    $body = ['client_id' => '7', 'action' => 'power_on', 'idempotency_key' => 'offline-contract-key'];
    $one = $real->request('POST', '42', 'power-actions', $body);
    $two = $real->request('POST', '42', 'power-actions', $body);
    $state = json_decode(file_get_contents($statePath), true, 64, JSON_THROW_ON_ERROR);
    check($one['id'] === $two['id'] && $state['writes'] === 1, 'HTTP idempotency contract failed');
    expectError(static fn() => $real->request('GET', '42', 'summary', ['client_id' => '8']), 'SERVICE_NOT_FOUND');
    $params = ['serviceid' => '42', 'userid' => '7', 'configoption1' => $origin, 'configoption2' => 'fixture', 'configoption3' => 'BAREPULSE_WHMCSTEST_SECRET'];
    check(barepulse_CreateAccount($params) === 'success', 'CreateAccount did not validate prebound mapping');
    check(barepulse_SuspendAccount($params) === 'success', 'Suspend did not confirm access-only policy');
    check(barepulse_UnsuspendAccount($params) === 'success', 'Unsuspend failed');
    check(str_contains(barepulse_TerminateAccount($params), 'SERVICE_BUSY'), 'Protected termination conflict hidden');
    $_SERVER['REQUEST_METHOD'] = 'GET'; $_POST = []; $_SESSION = [];
    $page = barepulse_ClientArea($params);
    check($page['templatefile'] === 'clientarea' && $page['vars']['bp']['authorized'], 'WHMCS ClientArea wrapper failed');
    check(!str_contains(json_encode($page, JSON_THROW_ON_ERROR), (string) getenv('BAREPULSE_WHMCSTEST_SECRET')), 'Secret leaked to template variables');
    $form = $page['vars']['bp'];
    $_SERVER['REQUEST_METHOD'] = 'POST';
    $_POST = ['bp_action' => 'console', 'bp_csrf' => $form['csrf'], 'bp_request_id' => $form['request_id'], 'client_id' => '8'];
    $consolePage = barepulse_ClientArea($params);
    check($consolePage['vars']['bp']['portal_url'] !== '' && $consolePage['vars']['bp']['error'] === '', 'Owned console form failed');
    $duplicate = barepulse_ClientArea($params);
    check(str_contains($duplicate['vars']['bp']['error'], 'TICKET_ALREADY_ISSUED'), 'Console ticket replay allowed');
    $response = $real->request('POST', '42', 'console', ['client_id' => '7']);
    check($real->validatePortalURL($response['portal_url']) === $response['portal_url'], 'Console ticket invalid');
    $state = json_decode(file_get_contents($statePath), true, 64, JSON_THROW_ON_ERROR);
    $state['mode'] = 'redirect'; file_put_contents($statePath, json_encode($state, JSON_THROW_ON_ERROR));
    expectError(static fn() => $real->request('GET', '42', 'power', ['client_id' => '7']), 'API_ERROR');
    $state = json_decode(file_get_contents($statePath), true, 64, JSON_THROW_ON_ERROR);
    $state['mode'] = 'malformed'; file_put_contents($statePath, json_encode($state, JSON_THROW_ON_ERROR));
    expectError(static fn() => $real->request('GET', '42', 'power', ['client_id' => '7']), 'RESPONSE_INVALID');
    echo 'PASS: ' . $checks . " total assertions including real loopback cURL/HMAC/lifecycle contracts.\n";
} finally {
    proc_terminate($process); proc_close($process);
    // Only known files created by this test are removed; no recursive cleanup.
    if (is_file($statePath)) unlink($statePath);
    if (is_file($logPath)) unlink($logPath);
}
