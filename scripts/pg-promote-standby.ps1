# Stage 5: database failover. Run it after the primary has died
# (for the demonstration:  docker compose kill -s SIGKILL postgres).
#
# What it does:
#   1. Makes sure the old primary can never accept writes again, even if its
#      container is started later. Otherwise there could be two primaries with
#      different data ("split brain").
#   2. Promotes the standby: from now on it accepts writes.
# booking needs no change: it knows both addresses and uses the one that accepts writes.
#
# Usage:  .\scripts\pg-promote-standby.ps1
. "$PSScriptRoot\_common.ps1"

Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    if ((Get-DbRole 'postgres') -eq 'primary') {
        Write-Host "The primary 'postgres' is alive and accepts writes. Nothing to fail over." -ForegroundColor Yellow
        Write-Host "To demonstrate a failure first run:  docker compose kill -s SIGKILL postgres"
        exit 1
    }
    $standbyRole = Get-DbRole 'postgres-standby'
    if ($standbyRole -eq 'primary') {
        Write-Host "'postgres-standby' is already the primary. Nothing to do." -ForegroundColor Yellow
        exit 0
    }
    if ($standbyRole -ne 'standby') {
        Write-Host "'postgres-standby' is not answering, so there is nothing to promote." -ForegroundColor Red
        exit 1
    }

    $started = Get-Date

    "Step 1: fencing the old primary (it will start read-only from now on)"
    docker compose stop postgres 2>$null | Out-Null
    docker compose run --rm --no-deps -T --entrypoint sh postgres -c 'touch /var/lib/postgresql/data/standby.signal && chown postgres:postgres /var/lib/postgresql/data/standby.signal' 2>$null
    if ($LASTEXITCODE -ne 0) {
        Write-Host "Could not fence the old primary. Not promoting, to avoid two primaries." -ForegroundColor Red
        exit 1
    }

    "Step 2: promoting postgres-standby"
    docker compose exec -T -u postgres postgres-standby pg_ctl promote -w
    if ($LASTEXITCODE -ne 0) { exit 1 }

    $role = Get-DbRole 'postgres-standby'
    $seconds = [math]::Round(((Get-Date) - $started).TotalSeconds, 1)
    ""
    if ($role -eq 'primary') {
        Write-Host "postgres-standby is now the PRIMARY. The switch took $seconds s." -ForegroundColor Green
        "The application works again: http://localhost:8080"
        "State of the pair:  .\scripts\ha-status.ps1"
    } else {
        Write-Host "Promotion was requested but postgres-standby does not accept writes yet." -ForegroundColor Red
        exit 1
    }
} finally {
    Pop-Location
}
