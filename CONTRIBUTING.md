# Contributing Guidelines

## Requirements

- Go 1.25+
- All new logic must include unit tests.
- No proprietary dependencies.

## Workflow

1. Fork repository
2. Create feature branch
3. Add tests
4. Ensure:

   go test ./... -tags unit

5. Submit PR

## Coding Standards

- Use structured logging
- Avoid global state
- Concurrency-safe code
- Prometheus metrics for new subsystems
