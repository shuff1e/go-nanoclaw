# Changelog

All notable changes to NanoClaw will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- TLS support for HTTP server (`tls_cert_file`/`tls_key_file` in config)
- Discord reconnection event handling with disconnect counting
- goreleaser configuration for automated multi-platform releases
- GitHub Actions release workflow (`.github/workflows/release.yml`)
- CONTRIBUTING.md and CHANGELOG.md
- Tests for `cron`, `hooks`, and `log` packages

### Changed
- Discord channel explicitly sets `ShouldReconnectOnError = true`

## [0.1.0] - 2025-01-01

### Added
- Initial release
- Multi-agent orchestration with controlled delegation
- Three execution modes: direct, plan_execute, plan_execute_verify
- Multi-provider LLM support (Anthropic, OpenAI-compatible)
- Three input channels: CLI, HTTP API (SSE), Discord bot
- Policy-bound tool execution with security sandboxing
- JSONL-based persistence (sessions, executions, traces, plans, memory)
- Prometheus metrics and structured tracing
- Health/readiness endpoints with LLM API reachability checks
- Context window management with automatic compaction
- Cron scheduler for periodic tasks
- Skill system with keyword/regex/always triggers
- Approval workflows for sensitive tool operations
- Graceful shutdown with task cancellation
