<?php
declare(strict_types=1);

// Optional real Smarty rendering, without a WHMCS installation or network API.
// SMARTY_CLASS_FILE must identify a separately obtained official Smarty runtime.
// Running with PHP -S serves only these generated, non-secret fixture pages.
$repo = dirname(__DIR__, 3);
$previewPages = ['/whmcs-chart-render.html', '/whmcs-empty-render.html'];
if (PHP_SAPI === 'cli-server') {
    $path = parse_url($_SERVER['REQUEST_URI'] ?? '', PHP_URL_PATH);
    if (($_SERVER['REQUEST_METHOD'] ?? '') !== 'GET'
        || !in_array($_SERVER['REMOTE_ADDR'] ?? '', ['127.0.0.1', '::1'], true)
        || !in_array($path, $previewPages, true)) { http_response_code(404); exit; }
    $file = $repo . '/work' . $path;
    if (!is_file($file)) { http_response_code(404); exit; }
    header('Content-Type: text/html; charset=utf-8');
    header("Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'none'; form-action 'none'; base-uri 'none'");
    header('Cache-Control: no-store');
    header('X-Content-Type-Options: nosniff');
    readfile($file);
    exit;
}
if (PHP_SAPI !== 'cli') { http_response_code(403); exit; }
$smartyFile = getenv('SMARTY_CLASS_FILE');
if (!$smartyFile) { echo "NOT_RUN: set SMARTY_CLASS_FILE to an official Smarty libs/Smarty.class.php (optional test).\n"; exit(0); }
if (!is_file($smartyFile)) { throw new RuntimeException('SMARTY_CLASS_FILE does not exist'); }
if (!extension_loaded('mbstring') || !extension_loaded('dom')) { throw new RuntimeException('Smarty rendering check requires PHP mbstring and DOM'); }
require_once $smartyFile;
require __DIR__ . '/contract.php';
$renderChecksStart = $checks;
$smartyClass = class_exists('Smarty\\Smarty') ? 'Smarty\\Smarty' : 'Smarty';
if (!class_exists($smartyClass)) { throw new RuntimeException('Unsupported Smarty loader'); }
$smarty = new $smartyClass();
$version = $smartyClass::SMARTY_VERSION;
$compile = $repo . '/work/smarty-compile-' . bin2hex(random_bytes(8));
if (!mkdir($compile, 0700, true) && !is_dir($compile)) { throw new RuntimeException('Cannot create isolated compile directory'); }
$smarty->setTemplateDir(dirname(__DIR__) . '/barepulse');
$smarty->setCompileDir($compile);
$smarty->setCacheDir($compile);
$smarty->caching = 0;
$smarty->force_compile = true;

// Fail on template notices/warnings rather than hiding undefined variables.
set_error_handler(static function (int $severity, string $message, string $file, int $line): bool {
    if (!(error_reporting() & $severity)) { return false; }
    throw new ErrorException($message, 0, $severity, $file, $line);
});
function renderFixture($smarty, array $traffic, string $name): string
{
    $fake = new FakeAPI(); $fake->traffic = $traffic; $fixtureSession = [];
    $area = new \BarePulse\ClientArea($fake, static fn($url) => $url);
    $view = $area->render('42', '7', 'GET', [], $fixtureSession);
    $view['authorized'] = true;
    $view['server_name'] = $name;
    $view['notice'] = '<script>fixture_notice()</script>';
    // Deliberately test attribute escaping with a clearly fake, unusable token.
    $smarty->assign('token', 'offline-fixture" autofocus onfocus="fixture()');
    $smarty->assign('bp', $view);
    return $smarty->fetch('clientarea.tpl');
}
function fixtureDOM(string $html): DOMXPath
{
    $document = new DOMDocument();
    $previous = libxml_use_internal_errors(true);
    try {
        $document->loadHTML('<?xml encoding="UTF-8">' . $html, LIBXML_NONET);
    } finally { libxml_clear_errors(); libxml_use_internal_errors($previous); }
    return new DOMXPath($document);
}
function fixtureDocument(string $content, string $version): string
{
    $version = htmlspecialchars($version, ENT_QUOTES, 'UTF-8');
    return '<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>BarePulse offline Smarty fixture</title><style>body{font:16px system-ui,sans-serif;color:#172a3a;max-width:1120px;margin:24px auto;padding:0 20px;background:#f6f8fa}header,.panel{background:white;border:1px solid #ccd5df;border-radius:8px;padding:20px;margin:16px 0}header{border-left:6px solid #b45309}h3,h4,h5{margin:12px 0}h5{font-size:18px}p{line-height:1.5}table{border-collapse:collapse;width:100%;font-size:14px}td,th{border:1px solid #ccd5df;padding:8px;text-align:left}caption{text-align:left;margin:8px 0}dt{font-weight:600}dd{margin:0 0 8px}button,select{padding:8px;margin:6px}svg{background:white}code{font-size:14px}.table-responsive{overflow:auto}</style></head><body><header><h1>Offline template fixture</h1><p>Actual BarePulse clientarea.tpl rendered by official Smarty ' . $version . '. All identities, counters, forms and tokens are isolated test data. This is not a WHMCS instance. Form submission and external connections are disabled by the preview server.</p><p><a href="/whmcs-chart-render.html">Measured / unknown intervals</a> · <a href="/whmcs-empty-render.html">Empty data</a></p></header>' . $content . '</body></html>';
}

try {
    $base = ['source_id' => 'offline-primary', 'starts_at' => '2026-10-15T00:00:00Z', 'ends_at' => '2026-10-15T00:01:00Z',
        'upload_bytes' => '6000000', 'download_bytes' => '12000000', 'quality' => 'normal', 'billable' => true];
    $fixtureTraffic = ['upload_bytes' => '56000000', 'download_bytes' => '103000000', 'used_bytes' => '159000000',
        'effective_quota_bytes' => '1000000000', 'remaining_bytes' => '841000000', 'period_start' => '2026-10-01T00:00:00Z', 'period_end' => '2026-11-01T00:00:00Z',
        'unallocated_upload_bytes' => '6000000', 'unallocated_download_bytes' => '12000000', 'known_intervals' => '4', 'unknown_intervals' => '1',
        'coverage_note' => 'Offline fixture; only confirmed intervals are known.', 'over_quota' => false,
        'intervals' => [$base,
            array_replace($base, ['starts_at' => '2026-10-15T00:01:00Z', 'ends_at' => '2026-10-15T00:02:00Z', 'upload_bytes' => '14000000', 'download_bytes' => '19000000']),
            array_replace($base, ['starts_at' => '2026-10-15T00:02:00Z', 'ends_at' => '2026-10-15T00:05:00Z', 'upload_bytes' => '36000000', 'download_bytes' => '72000000', 'quality' => 'recovered_gap']),
            array_replace($base, ['starts_at' => '2026-10-15T00:05:00Z', 'ends_at' => '2026-10-15T00:06:00Z', 'upload_bytes' => null, 'download_bytes' => null, 'quality' => 'counter_reset', 'billable' => false]),
            array_replace($base, ['starts_at' => '2026-09-30T23:59:00Z', 'ends_at' => '2026-10-01T00:01:00Z', 'quality' => 'boundary_unallocated', 'billable' => false])],
        'samples' => [['source_id' => 'offline-primary', 'observed_at' => '2026-10-15T00:06:00Z', 'quality' => 'collection_failed', 'error_code' => 'device_offline']]];
    // Keep the browser chart focused on a useful six-minute fixture range. The
    // separate boundary fixture below verifies unallocated rendering accurately.
    $browserTraffic = $fixtureTraffic;
    array_pop($browserTraffic['intervals']);
    $browserTraffic['unallocated_upload_bytes'] = '0'; $browserTraffic['unallocated_download_bytes'] = '0';
    $browserTraffic['known_intervals'] = '3';
    $unsafeName = '<img src=x onerror="fixture_attack()"> & offline server';
    $rendered = renderFixture($smarty, $browserTraffic, $unsafeName);
    $dom = fixtureDOM($rendered);
    check($dom->query('//*[local-name()="svg"]')->length === 1, 'Smarty did not render the SVG branch');
    check($dom->query('//*[local-name()="line"]')->length === 6, 'Smarty lost a measured upload/download segment');
    check($dom->query('//*[local-name()="line"][@stroke-dasharray="7 4"]')->length === 2, 'Recovered gap lost its dashed quality cue');
    check($dom->query('//*[local-name()="rect"][@fill-opacity="0.25"]')->length === 1, 'Unknown interval missing from rendered SVG');
    check($dom->query('//*[local-name()="circle"][@fill="#b91c1c"]')->length === 1, 'Failed sample marker missing from rendered SVG');
    check($dom->query('//img')->length === 0 && $dom->query('//*[@onerror or @onfocus or @autofocus]')->length === 0, 'Unescaped input became executable HTML attributes');
    check($dom->evaluate('string(//h4[1])') === $unsafeName, 'Escaping did not preserve literal untrusted server text');
    check($dom->query('//script')->length === 1 && str_contains($rendered, '&lt;script&gt;fixture_notice()&lt;/script&gt;'), 'Untrusted notice became a script element');
    check($dom->evaluate('string(//input[@name="token"]/@value)') === 'offline-fixture" autofocus onfocus="fixture()', 'Token attribute escaping failed');
    check(!str_contains($rendered, '{$bp.') && !str_contains($rendered, '{foreach'), 'Uncompiled Smarty syntax remains in output');
    foreach ($dom->query('//*[local-name()="line"]') as $line) {
        foreach (['x1', 'x2', 'y1', 'y2'] as $attribute) {
            $value = $line->getAttribute($attribute);
            check(preg_match('/^[0-9]+\.[0-9]{2}$/D', $value) === 1 && (float) $value <= 960, 'Rendered SVG contains invalid numeric coordinates');
        }
    }
    $boundaryHtml = renderFixture($smarty, $fixtureTraffic, 'Offline cross-period fixture');
    check(str_contains($boundaryHtml, 'boundary_unallocated / not billed'), 'Unallocated interval accounting was lost during Smarty rendering');
    $emptyHtml = renderFixture($smarty, [], 'Offline empty-data fixture');
    check(fixtureDOM($emptyHtml)->query('//*[local-name()="svg"]')->length === 0 && str_contains($emptyHtml, 'No valid traffic intervals or samples are available'), 'Empty-data Smarty branch fabricated a chart');
    $baselineHtml = renderFixture($smarty, ['samples' => $browserTraffic['samples']], 'Offline sample-only fixture');
    $baselineDOM = fixtureDOM($baselineHtml);
    check($baselineDOM->query('//*[local-name()="line"]')->length === 0 && $baselineDOM->query('//*[local-name()="circle"]')->length === 1, 'Sample-only branch fabricated interval segments');
    $smarty->assign('bp', ['authorized' => false, 'error' => '<img src=x onerror=fixture()>']);
    $deniedHtml = $smarty->fetch('clientarea.tpl');
    check(fixtureDOM($deniedHtml)->query('//form')->length === 0 && fixtureDOM($deniedHtml)->query('//img')->length === 0, 'Unauthorized/error branch exposes controls or raw HTML');
    // Save the actual generated template output. It contains only fixture data.
    file_put_contents($repo . '/work' . $previewPages[0], fixtureDocument($rendered, $version));
    file_put_contents($repo . '/work' . $previewPages[1], fixtureDocument($emptyHtml, $version));
    echo 'PASS: Smarty ' . $version . ' actual template rendering; ' . ($checks - $renderChecksStart) . " assertions including fixture API ownership checks.\n";
    echo "Generated work/whmcs-chart-render.html and work/whmcs-empty-render.html (offline fixtures only).\n";
} finally { restore_error_handler(); }
