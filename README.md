# bootlog-check

[![Latest release](https://img.shields.io/github/v/release/firmdura/bootlog-check)](https://github.com/firmdura/bootlog-check/releases/latest)
[![CI](https://github.com/firmdura/bootlog-check/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/firmdura/bootlog-check/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/firmdura/bootlog-check.svg)](https://pkg.go.dev/github.com/firmdura/bootlog-check)

A Go library and command-line tool that checks a saved firmware boot log against
rules you define. You can require messages to appear, set deadlines for them,
and reject logs that contain forbidden messages. The tool runs on Linux and
macOS and produces text, JSON, or JUnit reports. The same log and rules always
produce the same report.

A passing report means the supplied log meets your rules. It does not establish
that the capture is complete or that the device is healthy. bootlog-check reads
one saved boot at a time; it does not capture live output, flash or control
devices, diagnose failures, or separate a file into multiple boots.

## Installation

Prebuilt CLI archives for Linux and macOS on amd64 and arm64 are available from
the [v1.0.7 release](https://github.com/firmdura/bootlog-check/releases/tag/v1.0.7).
Download the archive for your operating system and CPU architecture. Check its
SHA-256 checksum against `SHA256SUMS`, then extract `bootlog-check` into a directory
on your `PATH`. The archives have no publisher signature; macOS binaries are not
Developer ID signed or notarized. You do not need Go to run a prebuilt CLI.

To build from source or use the library, install Go **1.27.4**, the version pinned
in `.go-version`, from [Go downloads](https://go.dev/dl/). Check the downloaded
archive against its published SHA-256 checksum and add Go's `bin` directory to
your `PATH`. The module requires at least Go **1.27.0** and uses only the standard
library.

Install the CLI with Go:

```sh
go install github.com/firmdura/bootlog-check/cmd/bootlog-check@v1.0.7
```

The executable goes into `GOBIN`, or `$(go env GOPATH)/bin` when `GOBIN` is unset.
Add that directory to your `PATH`. `@v1.0.7` selects this release, and
`bootlog-check --version` prints `bootlog-check 1.0.7`. The repository is public;
no GitHub credentials or private-module configuration are needed.

To build and verify a source checkout instead, run:

```sh
git clone --branch v1.0.7 https://github.com/firmdura/bootlog-check.git
cd bootlog-check
./scripts/verify.sh
./bin/bootlog-check --help
./bin/bootlog-check --version
```

## Quickstart

After building from source, run the following from the repository root. It
creates a sample log and a rules file, then checks the log in each output
format:

```sh
cat > rules.json <<'RULES'
{
  "schema_version": 1,
  "boot_start": {"pattern": "^BOOT$"},
  "milestones": [
    {"id": "ready", "pattern": "^APP_READY$", "within_ms": 1000}
  ],
  "forbidden": [
    {"id": "panic", "pattern": "(?i)kernel panic"}
  ]
}
RULES
cat > boot.log <<'LOG'
bootloader preamble
[1] BOOT
[2] APP_READY
LOG
./bin/bootlog-check --rules rules.json boot.log
./bin/bootlog-check --rules rules.json --format json boot.log > report.json
./bin/bootlog-check --rules rules.json --format junit boot.log > report.xml
```

All three commands exit with code 0. The timestamps are in seconds: `APP_READY`
appears exactly 1,000 ms after `BOOT`, so it meets the 1,000 ms deadline. A
match at the deadline passes. Rules match the message after the bracketed
timestamp. The checker reads the entire file, including any messages after
`APP_READY`. Reports include the first message that matched each check, so
handle reports with the same care as the source log.

Put all flags before the log path. Exit code **0** means every check passed;
**1** means the report is complete but contains failed checks. Exit code **2**
means the command could not complete because of an argument, input, or
operational error. Error messages go to stderr. Errors before report writing
produce no report; errors during writing can leave a partial file. Use the built
executable when you need its exact exit code, because `go run` can change it.
The [CI guide](docs/ci.md) shows how to save reports while preserving a failed
result.

## Library and further guides

Import `github.com/firmdura/bootlog-check` as package `bootlog`, and
`github.com/firmdura/bootlog-check/report` for rendering. API documentation is
available on pkg.go.dev for [bootlog](https://pkg.go.dev/github.com/firmdura/bootlog-check)
and [report](https://pkg.go.dev/github.com/firmdura/bootlog-check/report).
The [runnable library
example](examples/library/main.go) reads the rules and log, compiles the
patterns, evaluates the checks, and writes a report:

```sh
go run ./examples/library
go test ./examples/library
```

- [Using the CLI: log format, rules, limits, and reports](docs/usage.md)
- [Using the Go library: setup, examples, concurrency, and errors](docs/library.md)
- [Running in CI and verifying the repository](docs/ci.md)
- [Troubleshooting](docs/troubleshooting.md)
- [API and JSON schema reference](docs/schema-v1.md)
- [Parser details](docs/parsing.md), [rules and evaluation](docs/rules.md), and [report formats](report/README.md)
- [v1 behavior and interface contract](docs/v1-contract.md)

The [offline replay example](examples/replay/README.md) checks seven synthetic
scenarios in all three report formats. It compares the reports with
independently written expected results:

```sh
./scripts/demo.sh
go test ./integration/...
```

The [QEMU/Linux example](examples/qemu/README.md) includes logs and CLI reports
from six actual Linux boots: two normal boots, two with a delayed marker, one
with a missing marker, and one with a forbidden message. The saved logs were
checked independently against the reports. To reproduce those boots, use the
Linux ARM64 Docker environment described in that guide.

The quickstart and replay logs are synthetic. Ordinary tests run offline once Go
is installed and require no root access, hardware, Docker, or QEMU.

## Version compatibility

The v1 Go API and schema version 1 are stable. Compatible fixes and additions
use v1 releases; incompatible Go API changes require a new major version.
The report schema version is explicit and is independent of the release number.

v1.0.0 uses the `github.com/firmdura/bootlog-check` module path. Consumers of
the earlier personal-account module must update their imports and module
dependency to this path. Saved-log checking behavior and schema version 1
remain unchanged.

## About Firmdura

This repository contains a standalone verification utility from
[Firmdura](https://firmdura.com). Firmdura's commercial offer is shared embedded
Linux security maintenance through versioned security layers or patch overlays
for agreed user-space components.

## License

Licensed under the [Apache License 2.0](LICENSE).
