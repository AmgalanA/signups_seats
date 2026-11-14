# Shared helpers for the scripts in this folder. Not meant to be run by itself.

# Every service that has a firewall helper container "fw-<name>".
$FirewallNames = 'gateway', 'booking', 'notifier', 'postgres', 'postgres-standby', 'redis', 'rabbit1', 'rabbit2', 'rabbit3'

$DbNodes = 'postgres', 'postgres-standby'
$BrokerNodes = 'rabbit1', 'rabbit2', 'rabbit3'

# Runs one SQL statement on a database node and returns the plain answer.
# The statement must not contain quotes.
function Invoke-Sql([string]$Service, [string]$Sql) {
    $out = docker compose exec -T $Service sh -c "psql -U `$POSTGRES_USER -d `$POSTGRES_DB -Atc '$Sql'" 2>$null
    if ($LASTEXITCODE -ne 0) { return $null }
    return ("$out").Trim()
}

# 'primary' (accepts writes), 'standby' (read-only copy) or 'down'.
function Get-DbRole([string]$Service) {
    $answer = Invoke-Sql $Service 'SELECT pg_is_in_recovery()'
    if ($null -eq $answer) { return 'down' }
    if ($answer -eq 'f') { return 'primary' }
    return 'standby'
}

# Name of the database service that currently accepts writes, or $null.
function Get-PrimaryService {
    foreach ($s in $DbNodes) {
        if ((Get-DbRole $s) -eq 'primary') { return $s }
    }
    return $null
}

# Names of the services whose containers are running right now.
function Get-RunningServices {
    return @(docker compose ps --status running --format '{{.Service}}')
}
