# Contributing

This repository is a course and a product at once, so contributions fall into two kinds.

## Adding or improving a concept card

Every roadmap node has exactly one `### <Node name>` card in its module README, with the five parts from [`modules/TEMPLATE.md`](modules/TEMPLATE.md): *What it is* · *Alternatives compared* (table) · *Why it wins (and when it doesn't)* · *Problem it solves → value added* · *In the capstone*. Keep numbers durable (relative pricing, "order of 128k tokens") and cite primary sources. `make docs-check` verifies that every node in [`modules/ROADMAP-NODES.md`](modules/ROADMAP-NODES.md) still has a card; `mkdocs build --strict` verifies links.

## Changing the capstone

Go code lives under `internal/` with the package contract in [`ARCHITECTURE.md`](ARCHITECTURE.md). Rules that keep the repo honest:

1. `go.mod` stays dependency-free. Providers and stores are small `net/http` clients ([ADR-0009](adr/ADR-0009-raw-http-clients-over-vendor-sdks.md)). If you need a library, open an issue first.
2. Every feature works with `COPILOT_PROVIDER=mock`. If the mock cannot exercise your code path, extend the mock.
3. Agent tools are read-only and validate their inputs in code, not in the prompt ([ADR-0007](adr/ADR-0007-read-only-agent-tools-and-allowlists.md)).
4. Retrieval or prompt changes must keep `make eval` green (`hit@k ≥ 0.9`, answer accuracy ≥ 0.7 on the mock) and should update `data/eval/golden.jsonl` when they change what the corpus can answer.
5. New injection patterns go into `internal/safety/injection.go` **and** a case in `internal/safety/adversarial_test.go`.
6. Run `make check` before opening a PR — it is exactly what CI runs.

A decision that changes an ADR's consequences gets a new ADR that supersedes it; ADRs are never edited after acceptance.

## Python labs

Labs are standalone scripts in `labs/python/`, one concept each, that degrade gracefully (print `skipped: <reason>` and exit 0) when a key or server is missing. Pin new dependencies in `labs/python/requirements.txt` with compatible-release ranges.
