$ErrorActionPreference = "Stop"

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
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
    finally {
        Pop-Location
    }
}

& $binary @args
exit $LASTEXITCODE
