$ErrorActionPreference='Stop'
if (-not $env:TEST_DATABASE_URL) { throw 'TEST_DATABASE_URL required; integration tests must not silently skip' }
$unformatted = & gofmt -l backend frontend
if ($unformatted) { throw "Unformatted Go files: $unformatted" }
& go vet ./...
if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
& go test ./... -count=1
if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
& go build -trimpath -o bin/barepulse.exe ./backend/cmd/barepulse
if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
Get-ChildItem frontend -Filter '*.js' | ForEach-Object { & node --check $_.FullName; if ($LASTEXITCODE -ne 0) { throw "JavaScript syntax failed: $_" } }
