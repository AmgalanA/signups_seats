# Stage 5: restores the database from a backup made by db-backup.ps1.
# Everything written after that backup is replaced by the backup's contents.
# Usage:  .\scripts\db-restore.ps1                    (the newest backup)
#         .\scripts\db-restore.ps1 -File signup-20261114-120000.dump
param([string]$File)
. "$PSScriptRoot\_common.ps1"

Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    if (-not $File) {
        $newest = Get-ChildItem backups -Filter '*.dump' -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 1
        if (-not $newest) {
            Write-Host "There are no backups in the 'backups' folder. Make one: .\scripts\db-backup.ps1" -ForegroundColor Red
            exit 1
        }
        $File = $newest.Name
    }
    if ($File -notmatch '^[A-Za-z0-9._-]+$' -or -not (Test-Path "backups\$File")) {
        Write-Host "Backup file 'backups\$File' was not found." -ForegroundColor Red
        exit 1
    }

    $primary = Get-PrimaryService
    if (-not $primary) {
        Write-Host "No database node accepts writes right now, cannot restore." -ForegroundColor Red
        exit 1
    }

    $before = Invoke-Sql $primary 'SELECT count(*) FROM signups'
    $started = Get-Date
    # --clean --if-exists: drop the current tables first.
    # --single-transaction: either the whole restore happens or nothing changes.
    docker compose exec -T $primary sh -c "pg_restore -U `$POSTGRES_USER -d `$POSTGRES_DB --clean --if-exists --single-transaction /backups/$File"
    if ($LASTEXITCODE -ne 0) {
        Write-Host "pg_restore failed. The database was left as it was." -ForegroundColor Red
        exit 1
    }
    # The cached seat map describes the old data: drop it.
    docker compose exec -T redis redis-cli DEL seats:all | Out-Null

    $seconds = [math]::Round(((Get-Date) - $started).TotalSeconds, 1)
    $after = Invoke-Sql $primary 'SELECT count(*) FROM signups'
    Write-Host "Restored 'backups\$File' into '$primary' in $seconds s." -ForegroundColor Green
    "Sign-ups before the restore: $before"
    "Sign-ups after the restore:  $after"
} finally {
    Pop-Location
}
