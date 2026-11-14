# Stage 5: makes a backup of the database with pg_dump.
# The file appears in the "backups" folder of the project.
# Usage:  .\scripts\db-backup.ps1
. "$PSScriptRoot\_common.ps1"

Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    $primary = Get-PrimaryService
    if (-not $primary) {
        Write-Host "No database node accepts writes right now, cannot make a backup." -ForegroundColor Red
        exit 1
    }

    $name = "signup-$(Get-Date -Format 'yyyyMMdd-HHmmss').dump"
    $started = Get-Date
    # -Fc: the compact "custom" format, which pg_restore can read.
    docker compose exec -T $primary sh -c "pg_dump -U `$POSTGRES_USER -d `$POSTGRES_DB -Fc -f /backups/$name"
    if ($LASTEXITCODE -ne 0) {
        Write-Host "pg_dump failed." -ForegroundColor Red
        exit 1
    }
    $seconds = [math]::Round(((Get-Date) - $started).TotalSeconds, 1)

    $file = Get-Item "backups\$name"
    $signups = Invoke-Sql $primary 'SELECT count(*) FROM signups'
    Write-Host "Backup made from '$primary' in $seconds s." -ForegroundColor Green
    "File:      backups\$name ($($file.Length) bytes)"
    "Contains:  $signups sign-up(s) and the seat map"
    "Restore:   .\scripts\db-restore.ps1 -File $name"
} finally {
    Pop-Location
}
