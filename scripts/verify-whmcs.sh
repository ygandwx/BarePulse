#!/bin/sh
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
php_bin=${PHP_BIN:-php}
run_php() {
    if [ -n "${PHP_EXTENSION_DIR:-}" ]; then
        "$php_bin" -d "extension_dir=$PHP_EXTENSION_DIR" -d extension=curl "$@"
    else
        "$php_bin" "$@"
    fi
}
run_php -r 'if (!extension_loaded("curl")) { fwrite(STDERR, "PHP cURL extension required\n"); exit(1); }'
# Repository PHP filenames are controlled and contain no newline. Quote every path.
find integrations/whmcs -type f -name '*.php' -print | while IFS= read -r file; do
    run_php -l "$file"
done
run_php integrations/whmcs/tests/contract.php
run_php integrations/whmcs/tests/http_contract.php
printf '%s\n' 'WHMCS offline verification PASS (licensed WHMCS execution is separate).'
