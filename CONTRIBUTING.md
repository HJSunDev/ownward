# Contributing

Ownward is built against a deliberately narrow first-version boundary. Before changing behavior, read [the goal](docs/delivery/goal.md), [product requirements](docs/product/requirements.md), [architecture overview](docs/architecture/overview.md), and [development guidelines](docs/engineering/development-collaboration-guidelines.md).

Open an issue before proposing a change that alters product scope, durable asset semantics, the public core contract, or a frozen acceptance baseline. Implementation changes should solve one complete problem, preserve user assets, keep derived state rebuildable, and avoid abstractions without a current requirement.

During development, run the smallest test set that completely covers the change. Before submitting, run:

```sh
gofmt -l .
go vet ./...
go test ./...
go build ./...
```

Run the frozen performance or model-backed acceptance suites whenever the affected behavior participates in those conditions. Include the exact commands and results in the pull request; never include credentials, personal information, or private model inputs.

Commits should contain the largest independently understandable, buildable, testable, and reversible final-state subset. Do not commit unfinished states merely to create more history.
