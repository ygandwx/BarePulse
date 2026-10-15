#!/bin/sh
# Offline CI only: a new, isolated Compose project and simulated device assets.
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

python_bin=${PYTHON_BIN:-python3}
command -v docker >/dev/null 2>&1 || { printf '%s\n' 'NOT_RUN: Docker CLI unavailable.' >&2; exit 1; }
command -v "$python_bin" >/dev/null 2>&1 || { printf '%s\n' 'Python 3 is required.' >&2; exit 1; }
docker compose version >/dev/null

# Do not select a remote Docker endpoint through an inherited environment/context.
docker_endpoint=${DOCKER_HOST:-$(docker context inspect --format '{{.Endpoints.docker.Host}}')}
case "$docker_endpoint" in unix:///*) ;; *) printf '%s\n' 'Only a local Unix Docker socket is allowed for CI smoke.' >&2; exit 1;; esac
docker info >/dev/null 2>&1 || { printf '%s\n' 'NOT_RUN: Docker daemon unavailable.' >&2; exit 1; }

run_id=${GITHUB_RUN_ID:-local}
run_attempt=${GITHUB_RUN_ATTEMPT:-0}
case "$run_id" in local) ;; *[!0-9]*|'') printf '%s\n' 'Invalid GITHUB_RUN_ID.' >&2; exit 1;; esac
case "$run_attempt" in *[!0-9]*|'') printf '%s\n' 'Invalid GITHUB_RUN_ATTEMPT.' >&2; exit 1;; esac
nonce=$("$python_bin" -c 'import secrets; print(secrets.token_hex(8))')
case "$nonce" in *[!a-f0-9]*|'') printf '%s\n' 'Could not generate project nonce.' >&2; exit 1;; esac
project="barepulse-ci-$run_id-$run_attempt-$nonce"
readonly project nonce run_id run_attempt

POSTGRES_PORT=${POSTGRES_PORT:-15432}
APP_PORT=${APP_PORT:-18080}
export POSTGRES_PORT APP_PORT
"$python_bin" -c 'import os; p=[os.environ[x] for x in ("POSTGRES_PORT", "APP_PORT")]; assert all(x.isdecimal() and 1024 <= int(x) <= 65535 and int(x) != 5432 for x in p), "Use distinct unprivileged ports other than 5432"; assert int(p[0]) != int(p[1]), "Ports must differ"'
BASE_URL="http://127.0.0.1:$APP_PORT"
SMOKE_BASE_URL=$BASE_URL
ADMIN_EMAIL="ci-smoke-$nonce@example.test"
ADMIN_PASSWORD=$("$python_bin" -c 'import secrets; print(secrets.token_hex(24))')
POSTGRES_PASSWORD=$("$python_bin" -c 'import secrets; print(secrets.token_hex(24))')
ALLOW_REAL_DEVICES=false
ALLOW_DEVICE_WRITES=false
SECURE_COOKIES=false
export BASE_URL SMOKE_BASE_URL ADMIN_EMAIL ADMIN_PASSWORD POSTGRES_PASSWORD
export ALLOW_REAL_DEVICES ALLOW_DEVICE_WRITES SECURE_COOKIES
# Do not load a developer's .env, extra profiles, or an inherited compose file.
unset COMPOSE_FILE COMPOSE_PROFILES COMPOSE_ENV_FILES
compose() { docker compose --env-file /dev/null --project-name "$project" --file compose.yaml "$@"; }

# Never reuse a project. Only after all three inventories are empty may cleanup
# remove this invocation's named volumes. No global prune or database drop.
label="com.docker.compose.project=$project"
existing_containers=$(docker ps -aq --filter "label=$label")
existing_networks=$(docker network ls -q --filter "label=$label")
existing_volumes=$(docker volume ls -q --filter "label=$label")
if [ -n "$existing_containers$existing_networks$existing_volumes" ]; then
    printf '%s\n' 'Refusing to reuse an existing Compose project.' >&2
    exit 1
fi
owned_project=$project
readonly owned_project
cleanup() {
    result=$?
    trap - EXIT INT TERM
    if [ "$owned_project" != "barepulse-ci-$run_id-$run_attempt-$nonce" ] || [ "$project" != "$owned_project" ]; then
        printf '%s\n' 'Cleanup refused: project ownership validation failed.' >&2
        exit 1
    fi
    if [ "$result" -ne 0 ]; then
        compose logs --no-color --tail 80 app db >&2 || true
    fi
    if ! compose down --volumes --remove-orphans --timeout 15; then
        printf '%s\n' 'Cleanup failed; inspect only the reported CI project.' >&2
        result=1
    fi
    exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf 'Starting isolated offline Compose project: %s\n' "$project"
compose build app
compose up -d --wait --wait-timeout 90 db
compose run --rm --no-deps app migrate
compose run --rm --no-deps app bootstrap
compose up -d --no-deps app
"$python_bin" scripts/smoke-http.py
printf '%s\n' 'Compose build/migrate/bootstrap/HTTP smoke PASS; all device actions were simulated.'
