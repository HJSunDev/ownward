param(
    [switch]$InstallHooks,
    [string]$HooksTarget = ""
)

$ErrorActionPreference = "Stop"
$governanceRoot = $PSScriptRoot
$projectRoot = Split-Path -Parent (Split-Path -Parent $governanceRoot)
$binaryDirectory = Join-Path $governanceRoot "bin"
$binary = Join-Path $binaryDirectory "governance-cli.exe"
$candidate = Join-Path $binaryDirectory (".governance-cli-" + [Guid]::NewGuid().ToString("N") + ".exe")
$backup = Join-Path $binaryDirectory (".governance-cli-backup-" + [Guid]::NewGuid().ToString("N") + ".exe")

New-Item -ItemType Directory -Force -Path $binaryDirectory | Out-Null
try {
    Push-Location $governanceRoot
    try {
        & go test ./...
        if ($LASTEXITCODE -ne 0) { throw "governance tests failed" }
        & go build -trimpath -o $candidate ./cmd/governance-cli
        if ($LASTEXITCODE -ne 0) { throw "governance build failed" }

		& $candidate doctor | Out-Null
		if ($LASTEXITCODE -ne 0) { throw "candidate governance self-check failed" }
		$runtimeDirectory = Join-Path $governanceRoot "runtime"
		if (Test-Path -LiteralPath (Join-Path $runtimeDirectory "state.json")) {
			& $candidate validate-state-migration --source-dir $runtimeDirectory | Out-Null
			if ($LASTEXITCODE -ne 0) { throw "candidate governance migration validation failed" }
		}
    }
    finally {
        Pop-Location
    }
    if (Test-Path -LiteralPath $binary) {
        [IO.File]::Replace($candidate, $binary, $backup, $true)
        Remove-Item -Force -LiteralPath $backup -ErrorAction SilentlyContinue
    }
    else {
        [IO.File]::Move($candidate, $binary)
    }

    if ($InstallHooks) {
        $source = Join-Path $governanceRoot "hooks.runtime.json"
        $target = if ($HooksTarget) { $HooksTarget } else { Join-Path $projectRoot ".codex\hooks.json" }
        $sourceBytes = [IO.File]::ReadAllBytes($source)
        $same = (Test-Path -LiteralPath $target) -and ((Get-FileHash -Algorithm SHA256 -LiteralPath $source).Hash -eq (Get-FileHash -Algorithm SHA256 -LiteralPath $target).Hash)
        if (-not $same) {
            $targetDirectory = Split-Path -Parent $target
            New-Item -ItemType Directory -Force -Path $targetDirectory | Out-Null
            $temporary = Join-Path $targetDirectory (".hooks-" + [Guid]::NewGuid().ToString("N") + ".json")
            [IO.File]::WriteAllBytes($temporary, $sourceBytes)
            Move-Item -Force -LiteralPath $temporary -Destination $target
        }
    }
}
finally {
    if (Test-Path -LiteralPath $candidate) { Remove-Item -Force -LiteralPath $candidate -ErrorAction SilentlyContinue }
    if (Test-Path -LiteralPath $backup) { Remove-Item -Force -LiteralPath $backup -ErrorAction SilentlyContinue }
}
