# Contributing to vrhub-server

Thank you for your interest in vrhub-server. This document explains how to
set up a development environment, propose changes, and submit pull requests
that fit the project's conventions.

## Code of Conduct

By participating in this project you agree to keep the discussion focused,
respectful, and constructive. Harassment of any kind is not tolerated.

## Reporting Issues

- **Bug reports:** open a [bug report](https://github.com/LeGeRyChEeSe/vrhub-server/issues/new?template=bug_report.md)
  and include the exact `vrhub-server` version, operating system, Go
  version (`go version`) and the relevant log excerpt
  (`server-out.log`, `server-err.log`).
- **Feature requests:** open a [feature request](https://github.com/LeGeRyChEeSe/vrhub-server/issues/new?template=feature_request.md)
  and describe the user-facing problem before the proposed solution.
- **Security vulnerabilities:** follow the disclosure process in
  [`SECURITY.md`](SECURITY.md). Do **not** open a public issue.

## Development Setup

1. Install **Go 1.26.2** (or newer in the 1.26.x series).
2. Clone the repository:
   ```bash
   git clone https://github.com/LeGeRyChEeSe/vrhub-server.git
   cd vrhub-server
   ```
3. Verify the build and the test suite:
   ```bash
   go vet ./...
   go build ./cmd/server/
   go test ./...
   ```
4. For the admin UI, you only need a text editor — assets are embedded at
   build time via Go's `embed` package.

More detail in [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md).

## Coding Conventions

- **Language:** Go 1.26+ with `gofmt` formatting. Run `gofmt -w .` before
  committing.
- **Naming:** MixedCase for exported identifiers, mixedCase for locals,
  snake_case for JSON fields, snake_case plural for DB tables (full table
  in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)).
- **Errors:** wrap with `fmt.Errorf("context: %w", err)` so callers can use
  `errors.Is` / `errors.As`.
- **Logging:** use the package-level zerolog helper (`vlog.Get()`); never
  reach for `fmt.Println` outside the setup-wizard banner.
- **Tests:** standard `testing` package with `httptest` for handlers.
  Place test files next to the code under test, suffix `_test.go`.
- **Dependencies:** keep the dependency surface small. New direct
  dependencies must be justified in the PR description.

## Commit Messages

This project follows [Conventional Commits](https://www.conventionalcommits.org/).

| Prefix       | Use for                                              |
|--------------|------------------------------------------------------|
| `feat:`      | New user-facing functionality                        |
| `fix:`       | Bug fixes                                            |
| `docs:`      | Documentation only (README, docs/, comments)         |
| `refactor:`  | Code change that neither fixes a bug nor adds a feature |
| `perf:`      | Performance improvements                             |
| `test:`      | Adding or fixing tests                               |
| `chore:`     | Maintenance (CI, .gitignore, build scripts, deps)    |
| `style:`     | Formatting or UI tweaks without logic changes        |

Examples:

```
feat(api): add /admin/api/games/{releaseName}/revalidate endpoint
fix(setup): force setup mode when archive_password is missing
docs(readme): document the client integration flow
chore(repo): expand .gitignore and add README
```

A scope (`api`, `game`, `archive`, `setup`, ...) is **strongly recommended**
but not required. The subject line stays under 72 characters; the body
explains *why*, not *what*.

## Branch Naming

- `feat/<short-kebab-slug>` for new features
- `fix/<short-kebab-slug>` for bug fixes
- `chore/<short-kebab-slug>` for maintenance
- `docs/<short-kebab-slug>` for documentation-only changes
- `release/<semver>` for release prep

Keep branch names lowercase and dash-separated.

## Pull Request Flow

1. Fork the repository and create a feature branch from `main`.
2. Make focused commits; squash fixup commits locally before pushing.
3. Run the local validation commands:
   ```bash
   go vet ./...
   go build ./cmd/server/
   go test ./...
   ```
4. Push the branch and open a pull request against `main`.
5. Fill in the PR template: link the issue, describe the change, list
   the user-visible impact and call out any breaking change.
6. Address review comments by pushing additional commits; avoid force
   pushes once review has started.

## Release Process

Releases are tagged manually from `main` after the CI pipeline is green.
Tag names follow `vMAJOR.MINOR.PATCH`. The release notes are generated
from the conventional commit log and curated in
[`CHANGELOG.md`](CHANGELOG.md) before the tag is published.

## License

By contributing, you agree that your contributions will be licensed under
the [MIT License](LICENSE).
