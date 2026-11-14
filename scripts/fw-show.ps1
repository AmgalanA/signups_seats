# Stage 4: prints every firewall rule in the system, container by container.
# Usage:  .\scripts\fw-show.ps1
#         .\scripts\fw-show.ps1 -Counters    (also shows how many packets each rule matched)
param([switch]$Counters)
. "$PSScriptRoot\_common.ps1"

Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    foreach ($name in $FirewallNames) {
        ""
        "=== $name  (rules file: firewall/rules/$name.rules) ==="
        if ($Counters) {
            docker compose exec -T "fw-$name" iptables -L -n -v
        } else {
            docker compose exec -T "fw-$name" iptables -S
        }
    }
} finally {
    Pop-Location
}
