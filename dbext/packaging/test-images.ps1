[CmdletBinding()]
param(
    [ValidateSet('14','15','16','17','18')]
    [string[]]$PostgresMajor = @('14','15','16','17','18'),
    [ValidateSet('linux/amd64','linux/arm64')]
    [string[]]$Platform = @('linux/amd64')
)

$ErrorActionPreference = 'Stop'
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'docker CLI is required' }
foreach ($major in $PostgresMajor) {
    foreach ($platformName in $Platform) {
        $arch = ($platformName -split '/')[1]
        $tag = "agentsql/postgres-binder:$major-0.4-$arch"
        $name = "agentsql-binder-image-test-$major-$arch-$PID"
        try {
            docker run --detach --name $name --platform $platformName --env POSTGRES_PASSWORD=agentsql-test --env POSTGRES_DB=agentsql $tag | Out-Null
            if ($LASTEXITCODE -ne 0) { throw "container start failed: $tag" }
            $initComplete = $false
            for ($i=0; $i -lt 60; $i++) {
                $savedErrorAction = $ErrorActionPreference
                $ErrorActionPreference = 'SilentlyContinue'
                $containerLogs = docker logs --tail 100 $name 2>&1
                $ErrorActionPreference = $savedErrorAction
                if ($containerLogs -match 'PostgreSQL init process complete; ready for start up') { $initComplete = $true; break }
                Start-Sleep -Seconds 1
            }
            if (-not $initComplete) { throw "PostgreSQL initialization timeout: $tag" }

            $ready = $false
            for ($i=0; $i -lt 30; $i++) {
                # Wait for the final server, not docker-entrypoint's temporary
                # init server, before invoking the extension self-test.
                $savedErrorAction = $ErrorActionPreference
                $ErrorActionPreference = 'SilentlyContinue'
                docker exec $name psql -X --no-psqlrc -U postgres -d agentsql -c 'SELECT 1' *> $null
                $probeExit = $LASTEXITCODE
                $ErrorActionPreference = $savedErrorAction
                if ($probeExit -eq 0) { $ready = $true; break }
                Start-Sleep -Seconds 1
            }
            if (-not $ready) { throw "PostgreSQL readiness timeout: $tag" }
            docker exec --env PGUSER=postgres --env PGDATABASE=agentsql $name agentsql-binder-selftest
            if ($LASTEXITCODE -ne 0) { throw "derived image extension self-test failed: $tag" }
        } finally {
            docker rm --force $name *> $null
        }
    }
}
Write-Host 'All requested derived-image startup and extension self-tests passed; test containers were removed.'
