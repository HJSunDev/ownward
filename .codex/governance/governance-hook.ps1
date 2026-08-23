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
    $binaryDirectory = Join-Path $governanceRoot "bin"
    $binary = Join-Path $binaryDirectory "governance-cli.exe"
    $sourcePaths = @(
        (Join-Path $governanceRoot "go.mod")
    ) + @(Get-ChildItem -LiteralPath $governanceRoot -Filter "*.go" -Recurse | ForEach-Object { $_.FullName })

    $mustBuild = -not (Test-Path -LiteralPath $binary)
    if (-not $mustBuild) {
        $binaryWriteTime = (Get-Item -LiteralPath $binary).LastWriteTimeUtc
        $mustBuild = $sourcePaths | Where-Object { (Get-Item -LiteralPath $_).LastWriteTimeUtc -gt $binaryWriteTime } | Select-Object -First 1
    }

    if ($mustBuild) {
        New-Item -ItemType Directory -Force -Path $binaryDirectory | Out-Null
        Push-Location $governanceRoot
        try {
            & go build -trimpath -o $binary ./cmd/governance-cli
            if ($LASTEXITCODE -ne 0) { Exit-GovernanceFailure $LASTEXITCODE }
        }
        finally {
            Pop-Location
        }
    }

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
