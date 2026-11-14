# Stage 5: shows the state of the database pair and of the broker cluster.
# Usage:  .\scripts\ha-status.ps1
. "$PSScriptRoot\_common.ps1"

Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    $running = Get-RunningServices

    "=== Database ==="
    foreach ($node in $DbNodes) {
        if ($running -notcontains $node) {
            "{0,-18} DOWN (container is not running)" -f $node
            continue
        }
        $role = Get-DbRole $node
        $rows = Invoke-Sql $node 'SELECT count(*) FROM signups'
        switch ($role) {
            'primary' {
                $standbys = Invoke-Sql $node 'SELECT count(*) FROM pg_stat_replication WHERE state = $$streaming$$'
                "{0,-18} PRIMARY, accepts writes; sign-ups: {1}; standbys receiving changes: {2}" -f $node, $rows, $standbys
            }
            'standby' {
                $receiving = Invoke-Sql $node 'SELECT count(*) FROM pg_stat_wal_receiver'
                if ($receiving -eq '1') {
                    "{0,-18} STANDBY, read-only, receiving changes from the primary; sign-ups: {1}" -f $node, $rows
                } else {
                    "{0,-18} READ-ONLY and NOT receiving changes (old primary after a failover); sign-ups: {1}" -f $node, $rows
                }
            }
            default {
                "{0,-18} NOT ANSWERING (container runs, database does not respond)" -f $node
            }
        }
    }

    ""
    "=== Broker cluster ==="
    $alive = @($BrokerNodes | Where-Object { $running -contains $_ })
    foreach ($node in $BrokerNodes) {
        if ($running -contains $node) { "{0,-18} container running" -f $node } else { "{0,-18} DOWN (container is not running)" -f $node }
    }
    if ($alive.Count -eq 0) {
        "No broker node is running."
    } else {
        $ask = $alive[0]
        $status = docker compose exec -T $ask rabbitmqctl -q cluster_status --formatter json 2>$null | ConvertFrom-Json
        ""
        "Asked $ask. Nodes in the cluster: $($status.disk_nodes -join ', ')"
        "Nodes running now:               $($status.running_nodes -join ', ')"
        ""
        "Queue (name, leader node, nodes holding a live copy):"
        docker compose exec -T $ask rabbitmqctl -q list_queues name leader online 2>$null
    }
} finally {
    Pop-Location
}
