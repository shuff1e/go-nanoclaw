# Contributing to NanoClaw

## Getting Started

```bash
git clone https://github.com/nanoclaw/go-nanoclaw.git
cd go-nanoclaw
make build
make test
```

## Development Workflow

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/my-change`
3. Make your changes
4. Run checks: `make all` (lint + test + build)
5. Commit and push
6. Open a Pull Request

## Code Standards

- All code under `internal/` — this is an application, not a library
- Run `make lint` before committing (golangci-lint with 11 linters)
- Run `make test-race` to check for data races
- Tests use `t.TempDir()` for filesystem isolation
- Shared test helpers go in `internal/testutil/`

## Project Structure

```
cmd/nanoclaw/       CLI entry point
internal/agent/     Core agent loop and orchestration
internal/brain/     LLM provider abstraction
internal/channel/   Input/output channels (CLI, HTTP, Discord)
internal/config/    YAML configuration loading and validation
internal/gateway/   Central control plane
internal/hands/     Policy-bound tool execution
internal/store/     JSONL persistence layer
internal/runtime/   Execution context, errors, events, plans
```

## Commit Messages

- Use imperative mood: "Add feature" not "Added feature"
- Keep first line under 72 characters
- Reference issues when applicable: `Fix null pointer in cron scheduler (#42)`

## Pull Requests

- One logical change per PR
- Include tests for new functionality
- Ensure CI passes (test + lint + build)
- Update documentation if behavior changes

## Reporting Issues

Open an issue with:
- Steps to reproduce
- Expected vs actual behavior
- Go version and OS
- Relevant config (redact API keys)
