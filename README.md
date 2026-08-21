# Ownward

Personal information infrastructure owned by the user—not any intelligent entity—shared by intelligent entities of any present or future form, and continuously growing through use.

Ownward keeps the user's durable information assets independent from agents, models, indexes, protocols, and any one generation of its own kernel. External agents use one core to search, read, create, and update those assets; Ownward organizes their semantic relationships and keeps every derived representation rebuildable.

> Ownward is in first-version development. The repository does not yet publish a stable release.

## Architecture

```text
User <-> external agent <-> replaceable adapter <-> stable core contract
                                                       |
                                      +----------------+----------------+
                                      |                                 |
                              durable user assets              rebuildable derived state
```

Ownward does not include a user interface or an internal agent. The first adapter is an MCP server for existing software agents. Product intent, architecture invariants, and the exact first-version boundary are maintained in [docs](docs/README.md).

## Build

Requirements: Go 1.25 or newer. The first release target also requires the exact
EmbeddingGemma and llama.cpp artifacts pinned in
[the vector model selection](docs/research/vector-model-selection.md).

```sh
go test ./...
go build -trimpath -ldflags="-s -w" -o bin/ownward ./cmd/ownward
go run ./cmd/ownward-bundle \
  --model <embeddinggemma-300m-qat-Q8_0.gguf> \
  --runtime-archive <llama-b10488-bin-win-cpu-x64.zip> \
  --legal-root third_party \
  --output bin/embedding
go run ./cmd/ownward-release \
  --binary bin/ownward.exe \
  --embedding bin/embedding \
  --output dist/ownward-windows-amd64
```

On Windows, use `bin/ownward.exe` as the output path.

## Bundled vector capability

The release bundle includes the model, runtime, Gemma terms and use restrictions,
model notice and modification statement, and the llama.cpp license. Packaging and
release validation bind those legal materials to the distributed model. Product
runtime validates only the model, inference runtime, and vector-space artifacts;
it does not create or require a per-install terms-confirmation record. If the
local vector runtime is unavailable, durable assets, stable reads, and non-vector
retrieval remain available while vector state stays visibly pending.

Open-world semantic organization is supplied through Ownward's separate semantic
work contract by the connected external agent. Ownward does not require an
additional model endpoint or API key and never replaces missing understanding
with content-specific heuristics.

`OWNWARD_DATA_DIR` selects the user-asset directory. If omitted, Ownward uses the
operating system's user configuration directory. Never commit personal information
assets.

## Use

```sh
bin/ownward rules
bin/ownward create --content "A durable piece of user information"
bin/ownward search --query "What should I remember?"
bin/ownward backup --output ownward-backup.zip
```

Run the MCP adapter with:

```sh
bin/ownward mcp
```

The repository's [project-scoped Codex configuration](.codex/config.toml) launches
the built server with isolated assets under `.ownward/development`. The MCP server
itself supplies agents with Ownward's collaboration rules; adapter-private prompts
are not required.

## Verify

```sh
gofmt -l $(git ls-files '*.go')
go vet ./...
go test ./...
go build ./...
go build -trimpath -ldflags="-s -w -X main.version=COMMIT_SHA" -o bin/ownward ./cmd/ownward
python benchmarks/acceptance/suite/run.py check
python benchmarks/acceptance/suite/run.py self-check
go run ./cmd/ownward-production-storage --binary bin/ownward.exe --candidate COMMIT_SHA --workspace .tmp/production-storage --output production-storage-report.json
```

The [Ownward Acceptance Suite v1](benchmarks/acceptance/suite/README.md) binds one core-frontier optimization loop and exactly three evidence layers to the same release candidate: a deterministic core baseline, the fixed Ownward product dataset, and the pinned official LongMemEval-V2 benchmark. Historical harnesses are not independent completion gates.

See [Contributing](CONTRIBUTING.md), [Security](SECURITY.md), and the [Apache 2.0 license](LICENSE).
