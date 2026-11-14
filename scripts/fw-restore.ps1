# Stage 4: rebuilds the firewall of every container from firewall/rules/*.rules.
# Use it after fw-break.ps1, or after editing a rules file.
# Usage:  .\scripts\fw-restore.ps1
. "$PSScriptRoot\_common.ps1"

Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    foreach ($name in $FirewallNames) {
        docker compose exec -T "fw-$name" fw-apply
        if ($LASTEXITCODE -eq 0) {
            Write-Host "firewall of $name rebuilt from firewall/rules/$name.rules" -ForegroundColor Green
        } else {
            Write-Host "firewall of $name could NOT be rebuilt" -ForegroundColor Red
        }
    }
} finally {
    Pop-Location
}
