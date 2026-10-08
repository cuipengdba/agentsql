param(
    [switch]$RequireLive,
    [string]$ClientLibDir
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$archive = 'D:\ruanjiansheji\db-images\yashandb-image-23.4.1.109-linux-x86_64.tar.gz'
$moduleCache = Join-Path $env:USERPROFILE 'go\pkg\mod'
$transcript = 'C:\Users\Administrator\AppData\Local\Temp\agentsql-yashan-b71-verify.log'
$image = 'yashandb:yashandb-image-23.4.1.109-linux-x86_64'
$cacheVolume = 'agentsql-b71-go-cache'

function Step([string]$message) {
    Add-Content -LiteralPath $transcript -Encoding UTF8 -Value $message
    Write-Host $message
}

function Docker([string[]]$arguments) {
    # Driver output is deliberately suppressed: the transcript contains no credentials.
    & docker.exe @arguments 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Docker check failed (exit $LASTEXITCODE)." }
}

try {
    Set-Content -LiteralPath $transcript -Encoding UTF8 -Value 'YashanDB batch71 offline verification; no credential or license output'
    if (-not (Test-Path -LiteralPath $archive -PathType Leaf)) { throw 'Supplied YashanDB image archive is missing.' }
    Step 'PASS: supplied x86_64 image archive exists'
    $env:DOCKER_CONFIG = 'C:\Users\Administrator\AppData\Local\Temp\agentsql-docker-b71'
    if (-not (Test-Path -LiteralPath $env:DOCKER_CONFIG)) {
        New-Item -ItemType Directory -Path $env:DOCKER_CONFIG | Out-Null
    }
    Docker @('image', 'inspect', $image)
    Step 'PASS: supplied YashanDB image is loaded locally'
    $state = & docker.exe inspect yashan-v06 --format '{{.State.Status}}' 2>$null
    if ($LASTEXITCODE -ne 0 -or $state -ne 'running') { throw 'YashanDB container is not running.' }
    $mappedPort = & docker.exe port yashan-v06 '1688/tcp' 2>$null
    if ($LASTEXITCODE -ne 0 -or -not ($mappedPort -match ':1688$')) { throw 'YashanDB port 1688 is not mapped.' }
    Step 'PASS: yashan-v06 is running on port 1688'
    Docker @('exec', 'yashan-v06', 'test', '-x', '/data/yashan/yasdb_home/23.4.1.109/bin/yasql')
    Docker @('exec', 'yashan-v06', 'test', '-f', '/data/yashan/yasdb_home/23.4.1.109/include/yacli.h')
    Docker @('exec', 'yashan-v06', 'test', '-e', '/data/yashan/yasdb_home/23.4.1.109/lib/libyascli.so')
    Step 'PASS: yasql, C header and server-image client library are present (content not read)'
    if (-not (Test-Path -LiteralPath $moduleCache -PathType Container)) { throw 'Offline Go module cache is missing.' }
    Docker @('volume', 'create', $cacheVolume)
    $common = @('--rm', '--network', 'none', '--mount', "type=bind,source=$repo,target=/src,readonly", '--mount', "type=bind,source=$moduleCache,target=/gomod,readonly", '--mount', "type=volume,source=$cacheVolume,target=/buildcache", '--workdir', '/src', '--env', 'GOPROXY=off', '--env', 'GOTOOLCHAIN=local', '--env', 'GOMODCACHE=/gomod', '--env', 'GOCACHE=/buildcache', 'golang:1.26-bookworm')
    Docker (@('run') + $common + @('go', 'build', './...'))
    Step 'PASS: offline go build ./...'
    Docker (@('run') + $common + @('go', 'test', '-short', './internal/parser', './internal/engine', './internal/rules'))
    Step 'PASS: offline go test -short for parser, engine and rules'
    Step 'NOT RUN: pipeline and businessdb package tests need uncached github.com/moby/sys/sequential v0.7.0'
    # Compile the changed Yashan tests with their local fixtures only. The
    # excluded package tests cover unrelated databases and import Testcontainers.
    $overlayDir = 'C:\Users\Administrator\AppData\Local\Temp\agentsql-yashan-b71-overlay'
    New-Item -ItemType Directory -Force -Path $overlayDir | Out-Null
    $utf8 = [Text.UTF8Encoding]::new($false)
    [IO.File]::WriteAllText((Join-Path $overlayDir 'pipeline_stub.go'), "package pipeline`n", $utf8)
    [IO.File]::WriteAllText((Join-Path $overlayDir 'businessdb_stub.go'), "package businessdb`n", $utf8)
    $replacements = @{}
    foreach ($scope in @(@('internal\pipeline', 'pipeline'), @('internal\authorizedexecute\internal\businessdb', 'businessdb'))) {
        $folder = $scope[0]
        $packageName = $scope[1]
        $keep = @('yashan_test.go')
        if ($packageName -eq 'pipeline') { $keep += @('pipeline_test.go', 't25_observer_test.go', 'yashan_live_test.go') }
        foreach ($file in Get-ChildItem (Join-Path $repo $folder) -Filter '*_test.go' -File) {
            if ($keep -contains $file.Name) { continue }
            $relative = $file.FullName.Substring($repo.Length + 1).Replace('\', '/')
            $replacements[('/src/' + $relative)] = '/overrides/' + $packageName + '_stub.go'
        }
    }
    $overlayJSON = @{ Replace = $replacements } | ConvertTo-Json -Depth 5
    [IO.File]::WriteAllText((Join-Path $overlayDir 'focused.json'), $overlayJSON, $utf8)
    $focused = @($common[0..($common.Count - 2)]) + @('--mount', "type=bind,source=$overlayDir,target=/overrides,readonly", $common[-1])
    Docker (@('run') + $focused + @('go', 'test', '-overlay', '/overrides/focused.json', '-short', './internal/pipeline', './internal/authorizedexecute/internal/businessdb', '-run', '^TestYashan', '-count=1'))
    Step 'PASS: focused Yashan pipeline and businessdb tests with unrelated package tests excluded'
    if ($RequireLive) {
        if ([string]::IsNullOrWhiteSpace($ClientLibDir) -or -not (Test-Path -LiteralPath (Join-Path $ClientLibDir 'libyascli.so') -PathType Leaf)) {
            throw 'A compatible standalone YashanDB C client lib directory is required for live verification.'
        }
        if ([string]::IsNullOrWhiteSpace($env:YASHAN_PASSWORD)) { throw 'YASHAN_PASSWORD is required for live verification.' }
        $client = (Resolve-Path -LiteralPath $ClientLibDir).Path
        $tagged = @($common[0..($common.Count - 2)]) + @('--env', 'CGO_ENABLED=1', $common[-1])
        Docker (@('run') + $tagged + @('go', 'build', '-tags', 'yashan', './...'))
        Step 'PASS: offline CGO go build -tags yashan ./...'
        $live = @('--rm', '--mount', "type=bind,source=$repo,target=/src,readonly", '--mount', "type=bind,source=$moduleCache,target=/gomod,readonly", '--mount', "type=bind,source=$client,target=/client/lib,readonly", '--mount', "type=volume,source=$cacheVolume,target=/buildcache", '--workdir', '/src', '--env', 'GOPROXY=off', '--env', 'GOTOOLCHAIN=local', '--env', 'GOMODCACHE=/gomod', '--env', 'GOCACHE=/buildcache', '--env', 'CGO_ENABLED=1', '--env', 'LD_LIBRARY_PATH=/client/lib', '--env', 'AGENTSQL_YASHAN_E2E=1', '--env', 'YASHAN_PASSWORD', '--env', 'YASHAN_HOST=host.docker.internal', 'golang:1.26-bookworm')
        $probe = & docker.exe @(@('run') + $live + @('go', 'run', '-tags', 'yashan_probe', './scripts/yashan-native-probe.go')) 2>$null
        if ($LASTEXITCODE -ne 0) { throw "Native YashanDB probe failed (exit $LASTEXITCODE)." }
        foreach ($line in $probe) { Step $line }
        Step 'PASS: live native driver probe completed'
        $liveFocused = @($live[0..($live.Count - 2)]) + @('--mount', "type=bind,source=$overlayDir,target=/overrides,readonly", $live[-1])
        Docker (@('run') + $liveFocused + @('go', 'test', '-tags', 'yashan', '-overlay', '/overrides/focused.json', '-short', './internal/authorizedexecute/internal/businessdb', '-run', '^TestYashanDiscoveryE2E$', '-count=1'))
        Step 'PASS: live YashanExecutor pool/session SELECT, metadata, write and EXPLAIN guards'
        Docker (@('run') + $liveFocused + @('go', 'test', '-tags', 'yashan', '-overlay', '/overrides/focused.json', '-short', './internal/pipeline', '-run', '^TestYashanPipelineRealE2E$', '-count=1'))
        Step 'PASS: live Yashan pipeline SELECT, lineage, R010, masking and three audits'
    } else {
        Step 'NOT RUN: live native driver probe (no standalone client runtime supplied)'
    }
    Step 'PASS: batch71 script completed'
} catch {
    Step ('FAIL: ' + $_.Exception.Message)
    exit 1
}
