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
bin/ownward setup
bin/ownward create --content "A durable piece of user information"
bin/ownward search --query "What should I remember?"
bin/ownward backup --output ownward-backup.zip
```

Run the MCP adapter with:

```sh
bin/ownward mcp
```

The repository's [project-scoped Codex configuration](.codex/config.toml) launches
the built adapter with isolated assets under `.ownward/development`. `mcp` is a
connect-or-start stdio adapter: the first client starts one authenticated loopback
core for that data directory, and later clients connect to the same authoritative
core. Client exit does not create or destroy private product state. The MCP server
supplies agents with Ownward's collaboration rules; adapter-private prompts are not
required.

On a host that supports MCP form elicitation, the connector handles owner setup,
access approval and management confirmation inside the host, then resumes the
original operation. Credentials stay in the OS-protected connector store and never
enter tool results. Direct CLI use initializes the local owner with `setup`;
`recover-owner` restores that protected management connection. These local commands
belong to the trusted OS-user boundary. See [user control](docs/modules/information-control/README.md)
for authorization, revocation and forgetting guarantees.

### Codex integration

The Windows release is also a native Codex plugin. Install from its extracted
directory using Codex's normal plugin flow:

```sh
codex plugin marketplace add <release-directory>
codex plugin add ownward@ownward
```

Open Codex and review the three Ownward hooks in its normal trust dialog. This
integration has been verified with Codex CLI 0.149.0; a host must support native
plugins, MCP tool hooks and MCP confirmation forms. An unsupported host or an
untrusted hook does not provide automatic material checking. `/hooks` shows the
enabled state. Configure this connection once, using the plugin instead of a
duplicate manual MCP entry.

The plugin supplies the existing tools and rules, remembers a bounded set of
recent source references, and checks them when work resumes or a new user turn
starts. Changed sources are reread in the original task; checks require no model
call and do not rescan the corpus. Full and fragment reads include explicit
clarification locations, so a current fragment cannot silently hide a correction
elsewhere in its source. See [information changes](docs/modules/information-change/README.md).

Local use needs no connection file. To select a remote information system, run
`bin/ownward.exe codex-configure --connection connection.json` from the release
directory, then reopen Codex. The file supplies public trust information only;
the existing connector handles authorization. Run `codex-configure` without
options to return to local use. Source references remain independent of location.

### Connect from another location

Install the Windows release on a user-controlled, reachable host. Run the installation
command with that host's administrator authorization; network ingress remains a
deployment responsibility. Installation reports local readiness, not Internet reachability.

```sh
ownward.exe service-install --data-dir <service-dir> --endpoint https://ownward.example:8443 --listen :8443 --output source.json
ownward.exe service-invite --data-dir <service-dir> --output connection.json
```

On the agent's host, configure the MCP stdio command as
`ownward.exe connect --connection connection.json`. Only the connector binary is
needed there. For the first management connection, run
`ownward.exe service-approve --data-dir <service-dir>` on the service host, compare
the marker displayed on the requesting host, then approve with
`--id <request-id> --marker <marker> --approve`. Subsequent connections and decisions
use the trusted agent host's `ownward_connect` tool and MCP confirmation form.
Connection files contain public trust information, never credentials; deliver them
through a channel the user controls.

To move, install the same release at an empty destination with
`service-install --data-dir <new-dir> --endpoint <new-https-address> --receive-from source.json --output destination.json`.
The trusted agent host's `ownward_migrate` tool accepts that destination descriptor
and handles confirmation, transfer and continuation. It preserves assets, grants,
valid organization results and unfinished operations without model calls. The old
service permanently stops serving data before the destination activates. If a device
missed the location handoff, retain its original `--connection` file and add
`--location destination.json`; its existing identity and permissions remain intact.

The OS service restarts after failure; rerunning the identical installation command
resumes a partial installation. `service-recover --data-dir <service-dir>` restores
the protected local management entry. See [access and migration](docs/modules/access/README.md)
for interruption, cancellation and cleanup guarantees.

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

The [Ownward Acceptance Suite v1](benchmarks/acceptance/suite/README.md) binds one core-frontier optimization loop and exactly three evidence layers to the same release candidate: a deterministic core baseline, the fixed Ownward product dataset, and the pinned official cleaned LongMemEval-S benchmark. Historical harnesses are not independent completion gates.

See [Contributing](CONTRIBUTING.md), [Security](SECURITY.md), and the [Apache 2.0 license](LICENSE).
