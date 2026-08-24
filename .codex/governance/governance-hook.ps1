$ErrorActionPreference = "Stop"

$isHook = $args.Count -ge 1 -and $args[0] -eq "hook"

function Exit-GovernanceFailure([int]$Code) {
    if ($isHook) {
        [Console]::Out.WriteLine("{}")
        exit 0
    }
    exit $Code
}

try {
    $governanceRoot = $PSScriptRoot
    $binary = Join-Path $governanceRoot "bin\governance-cli.exe"
    if (-not (Test-Path -LiteralPath $binary)) { Exit-GovernanceFailure 1 }

    if ($isHook) {
        $output = @(& $binary @args)
        $code = $LASTEXITCODE
        if ($code -ne 0) { Exit-GovernanceFailure $code }
        $output | ForEach-Object { [Console]::Out.WriteLine($_) }
        exit 0
    }

    & $binary @args
    exit $LASTEXITCODE
}
catch {
    if (-not $isHook) {
        [Console]::Error.WriteLine($_.Exception.Message)
    }
    Exit-GovernanceFailure 1
}
