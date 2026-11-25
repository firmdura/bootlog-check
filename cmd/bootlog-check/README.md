# Command usage

Run this example from the repository root with the Go version pinned in
`.go-version` on your `PATH`. It builds the command, creates a small sample log
and rules file, and checks the log. After Go is installed, the example needs no
network access or hardware.

```sh
go build -o ./bin/bootlog-check ./cmd/bootlog-check
example_dir=$(mktemp -d)
cat > "$example_dir/rules.json" <<'RULES'
{
  "schema_version": 1,
  "boot_start": {"pattern": "^BOOT$"},
  "milestones": [{"id": "ready", "pattern": "^READY$", "within_ms": 1000}],
  "forbidden": [{"id": "panic", "pattern": "PANIC"}]
}
RULES
printf '[1] BOOT\n[2] READY\n' > "$example_dir/boot.log"
./bin/bootlog-check --rules "$example_dir/rules.json" --format json "$example_dir/boot.log"
rm -r "$example_dir"
```

The check exits with code 0 and writes a complete version 1 JSON report. `READY`
appears exactly one second after `BOOT`, so it meets the 1,000 ms deadline. A
match exactly at the deadline passes.

To try a failed check, change the `READY` timestamp from `[2]` to `[2.001]` in
the example before running it. The command will write a complete failed report
and exit with code 1. To try an input error, append `[2.x] broken` to the sample
log before checking it. The command will exit with code 2, write an error
message to stderr, and produce no report. Malformed input is an error even after
readiness.

Run `./bin/bootlog-check --help` (or `./bin/bootlog-check -h`) for the full flag
reference. Put every flag before the single log path, and use `--` before a log
filename that begins with `-`. When checking a log, supply `--rules FILE`, and
quote paths that contain spaces. `--format` accepts `text` (the default),
`json`, or `junit`.

If you only have the rule file, run `bootlog-check --check-rules rules.json`
without a log or other flags. Valid rules exit 0 without a report; invalid
rules exit 2 with a diagnostic on stderr.

The three limit flags accept positive ASCII decimal integers up to
9,223,372,036,854,775,807; type the digits without commas. Signs, fractions,
exponents, hexadecimal notation, and zero are invalid. Leading zeros still mean
decimal. The defaults are 16 MiB of raw log bytes, 100,000 physical lines, and
64 KiB per line excluding LF or CRLF line endings.

Both inputs must be local regular files; symlinks to such files are accepted.
The tool cannot read from standard input (`-`), URLs, directories, named pipes
(FIFOs), device ports, or compressed archives. It validates the whole log,
including forbidden messages or malformed lines after readiness, and closes its
inputs before writing a report.

Ctrl-C or SIGTERM requests cancellation. The command checks for cancellation
between reads and processing phases, but that check cannot interrupt a blocked
input/output call or synchronous evaluation already in progress.

Stdout contains the report, or help or version text when requested. Error
messages go to stderr. Exit code 0 means all checks passed; code 1 means the
report is complete but contains failed checks. Code 2 means the command could
not complete because of invalid arguments or input, an exceeded limit,
cancellation, or an I/O error.

Errors before report writing produce no report. An output error, including a
short write or closed pipe, returns 2 and may leave a partial report. Discard
that incomplete file. An empty input file is valid, but its report fails the
boot-start check and the command returns 1.

Use shell redirection to save a report, preserving the actual command status:

```sh
bootlog_status=0
./bin/bootlog-check --rules rules.json --format junit boot.log > report.xml || bootlog_status=$?
printf 'bootlog-check exit: %s\n' "$bootlog_status"
exit "$bootlog_status"
```

A pass means the supplied capture meets the configured rules. It does not
establish that the capture is complete or that the device is healthy. Reports
include matched messages, so apply the source log's data handling policy to
them. See the [report format reference](../../report/README.md) and [v1.0
contract](../../docs/v1-contract.md) for matching and timing semantics.
