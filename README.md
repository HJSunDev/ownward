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

Requirements: Go 1.25 or newer.

```sh
go test ./...
go build -trimpath -ldflags="-s -w" -o bin/ownward ./cmd/ownward
```

On Windows, use `bin/ownward.exe` as the output path.

## Configure semantic organization

Ownward accepts an OpenAI-compatible Chat Completions and Embeddings endpoint:

```text
OWNWARD_MODEL_BASE_URL=https://api.openai.com/v1
OWNWARD_MODEL_API_KEY=...
OWNWARD_CHAT_MODEL=...
OWNWARD_EMBEDDING_MODEL=...
OWNWARD_EMBEDDING_DIMENSIONS=384
```

Without model configuration, Ownward remains usable with a deterministic degraded fallback and marks organization results as `degraded`; that mode does not represent the product's target organization quality.

The configured model endpoint receives information being organized, selected related candidates, and search queries. Choose a provider whose data handling is acceptable for the user's personal information; see [Security](SECURITY.md).

`OWNWARD_DATA_DIR` selects the local data directory. If omitted, Ownward uses the operating system's user configuration directory. Never commit model credentials or personal information assets.

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

The repository's [project-scoped Codex configuration](.codex/config.toml) launches the same server during development and inherits the Ownward environment variables listed above. The MCP server itself supplies agents with Ownward's collaboration rules; adapter-private prompts are not required.

## Verify

```sh
gofmt -l .
go vet ./...
go test ./...
go build ./...
go run ./cmd/ownward-performance --scale 100000 --dimensions 384
```

The model-backed product acceptance suite deliberately refuses to run without a real semantic provider:

```sh
go run ./cmd/ownward-acceptance
```

See [Contributing](CONTRIBUTING.md), [Security](SECURITY.md), and the [Apache 2.0 license](LICENSE).
