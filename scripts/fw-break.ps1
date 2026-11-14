# Stage 4: removes one rule the application needs, to show the errors that follow.
# By default it removes "booking may connect to PostgreSQL".
# Usage:  .\scripts\fw-break.ps1
#         .\scripts\fw-break.ps1 -Service booking -Rule booking-to-redis
# Undo:   .\scripts\fw-restore.ps1
param(
    [string]$Service = 'booking',
    [string]$Rule = 'booking-to-postgres'
)

Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    docker compose exec -T "fw-$Service" fw-remove $Rule
    if ($LASTEXITCODE -ne 0) { exit 1 }

    Write-Host ""
    Write-Host "Rules of $Service now:" -ForegroundColor Cyan
    docker compose exec -T "fw-$Service" iptables -S
    Write-Host ""
    Write-Host "Now try the application: http://localhost:8080" -ForegroundColor Yellow
    Write-Host "Errors appear in:  docker compose logs --tail 20 $Service"
    Write-Host "Put the rule back: .\scripts\fw-restore.ps1"
} finally {
    Pop-Location
}
