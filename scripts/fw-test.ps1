# Stage 4: tries allowed and forbidden connections and shows which ones pass.
# Every check opens a TCP connection from inside the "from" container.
# A blocked connection takes 2 seconds, because the firewall silently drops it.
# Usage:  .\scripts\fw-test.ps1

$checks = @(
    # Allowed: the connections the application needs.
    @{ From = 'gateway';  To = 'booking';          Addr = '172.28.2.20'; Port = 8080;  Expect = 'pass' }
    @{ From = 'booking';  To = 'postgres';         Addr = '172.28.3.40'; Port = 5432;  Expect = 'pass' }
    @{ From = 'booking';  To = 'postgres-standby'; Addr = '172.28.3.41'; Port = 5432;  Expect = 'pass' }
    @{ From = 'booking';  To = 'redis';            Addr = '172.28.3.50'; Port = 6379;  Expect = 'pass' }
    @{ From = 'booking';  To = 'rabbit1';          Addr = '172.28.3.61'; Port = 5672;  Expect = 'pass' }
    @{ From = 'booking';  To = 'rabbit3';          Addr = '172.28.3.63'; Port = 5672;  Expect = 'pass' }
    @{ From = 'notifier'; To = 'rabbit2';          Addr = '172.28.3.62'; Port = 5672;  Expect = 'pass' }
    # Allowed: what the standby database and the broker cluster need (stage 5).
    @{ From = 'postgres-standby'; To = 'postgres'; Addr = '172.28.3.40'; Port = 5432;  Expect = 'pass' }
    @{ From = 'rabbit1';  To = 'rabbit2 cluster';  Addr = '172.28.3.62'; Port = 25672; Expect = 'pass' }
    @{ From = 'rabbit3';  To = 'rabbit1 cluster';  Addr = '172.28.3.61'; Port = 25672; Expect = 'pass' }
    # Forbidden: gateway is not in the data network at all.
    @{ From = 'gateway';  To = 'postgres';         Addr = '172.28.3.40'; Port = 5432;  Expect = 'blocked' }
    @{ From = 'gateway';  To = 'redis';            Addr = '172.28.3.50'; Port = 6379;  Expect = 'blocked' }
    @{ From = 'gateway';  To = 'rabbit1';          Addr = '172.28.3.61'; Port = 5672;  Expect = 'blocked' }
    # Forbidden: same network, so only the firewall stops these.
    @{ From = 'notifier'; To = 'postgres';         Addr = '172.28.3.40'; Port = 5432;  Expect = 'blocked' }
    @{ From = 'notifier'; To = 'postgres-standby'; Addr = '172.28.3.41'; Port = 5432;  Expect = 'blocked' }
    @{ From = 'notifier'; To = 'redis';            Addr = '172.28.3.50'; Port = 6379;  Expect = 'blocked' }
    @{ From = 'notifier'; To = 'booking';          Addr = '172.28.2.20'; Port = 8080;  Expect = 'blocked' }
    @{ From = 'booking';  To = 'gateway';          Addr = '172.28.2.10'; Port = 80;    Expect = 'blocked' }
    @{ From = 'booking';  To = 'rabbit1 console';  Addr = '172.28.3.61'; Port = 15672; Expect = 'blocked' }
    @{ From = 'booking';  To = 'rabbit2 cluster';  Addr = '172.28.3.62'; Port = 25672; Expect = 'blocked' }
    # Forbidden: a data store must not be able to reach anything it does not need.
    @{ From = 'postgres'; To = 'postgres-standby'; Addr = '172.28.3.41'; Port = 5432;  Expect = 'blocked' }
    @{ From = 'postgres'; To = 'redis';            Addr = '172.28.3.50'; Port = 6379;  Expect = 'blocked' }
    @{ From = 'redis';    To = 'postgres';         Addr = '172.28.3.40'; Port = 5432;  Expect = 'blocked' }
    @{ From = 'rabbit1';  To = 'booking';          Addr = '172.28.3.20'; Port = 8080;  Expect = 'blocked' }
    @{ From = 'rabbit2';  To = 'postgres';         Addr = '172.28.3.40'; Port = 5432;  Expect = 'blocked' }
)

function Test-HostPort([int]$Port) {
    $client = New-Object Net.Sockets.TcpClient
    try {
        $task = $client.ConnectAsync('127.0.0.1', $Port)
        return ($task.Wait(2000) -and $client.Connected)
    } catch {
        return $false
    } finally {
        $client.Close()
    }
}

Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    $rows = @()

    # A missing test tool would look exactly like a blocked connection,
    # so first make sure every container can run the check at all.
    foreach ($from in ($checks | ForEach-Object { $_.From } | Select-Object -Unique)) {
        docker compose exec -T $from sh -c 'command -v nc' 2>$null | Out-Null
        if ($LASTEXITCODE -ne 0) {
            Write-Host "Cannot test from '$from': the container is not running or has no 'nc' tool." -ForegroundColor Red
            exit 1
        }
    }

    # From the laptop: only gateway (8080) is open to users; the database port is not published.
    foreach ($h in @(@{ To = 'gateway'; Port = 8080; Expect = 'pass' }, @{ To = 'postgres'; Port = 5432; Expect = 'blocked' })) {
        $result = if (Test-HostPort $h.Port) { 'pass' } else { 'blocked' }
        $rows += [pscustomobject]@{ From = 'laptop'; To = $h.To; Address = "localhost:$($h.Port)"; Expected = $h.Expect; Result = $result; OK = ($result -eq $h.Expect) }
    }

    foreach ($c in $checks) {
        docker compose exec -T $c.From nc -z -w 2 $c.Addr $c.Port 2>$null | Out-Null
        $result = if ($LASTEXITCODE -eq 0) { 'pass' } else { 'blocked' }
        $rows += [pscustomobject]@{ From = $c.From; To = $c.To; Address = "$($c.Addr):$($c.Port)"; Expected = $c.Expect; Result = $result; OK = ($result -eq $c.Expect) }
    }

    $rows | Format-Table -AutoSize

    $wrong = @($rows | Where-Object { -not $_.OK })
    if ($wrong.Count -eq 0) {
        Write-Host "All $($rows.Count) checks behave as the firewall rules say." -ForegroundColor Green
        exit 0
    } else {
        Write-Host "$($wrong.Count) of $($rows.Count) checks differ from what was expected." -ForegroundColor Red
        exit 1
    }
} finally {
    Pop-Location
}
