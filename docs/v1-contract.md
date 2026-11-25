# v1.0 behavior and interface contract

This reference describes how the library and CLI interpret saved logs and rules.
See the [README](../README.md) for installation and examples, and the
[API and schema reference](schema-v1.md) for field names, JSON representations,
statuses, error types, validation rules, and runnable Go examples.

## Purpose and scope

`bootlog-check` checks one saved boot log against project-defined milestones,
deadlines, and forbidden messages. It provides a reusable Go library and a CLI
with deterministic text, JSON, and JUnit output on Linux and macOS. The firmware's
architecture does not affect checking a saved log.

v1.0 accepts local UTF-8 text logs and a strict JSON rule file. It does not capture
UART output, reset or flash devices, run target commands, split multiple boots,
infer timestamp formats, compare statistical baselines, or diagnose root causes.
A passing result does not prove that a capture is complete or a device is healthy.
The QEMU example includes capture scripts, but those scripts are separate from
the product API.

## Saved-log format

The parser reads the complete input line by line and stores accepted records in
memory. Memory use grows with message size and record count, subject to explicit
limits. The parser must enforce those limits before buffers can grow without
bound; it must not read an unbounded input with `ReadAll`.

### Lines and messages

LF and CRLF line endings are accepted, as is a final line without a line ending.
The parser removes the line ending, including the single CR that belongs to CRLF,
and preserves every other character. Each record keeps its original line number,
starting at 1.

Only empty lines are skipped. They still count toward input and line limits.
Whitespace-only lines remain messages without timestamps, with their text
unchanged. Valid control characters, including ANSI escape sequences, also remain
unchanged for matching. Report writers are responsible for escaping unsafe display
characters.

### Timestamp prefixes

A timestamped line starts in column one with `[`, followed by zero or more ASCII
spaces or tabs, a nonnegative number of seconds written with one or more ASCII
decimal digits, and `]`. The number may
have a decimal point followed by 1 to 9 fractional digits. The closing bracket
must immediately follow the number, with no intervening whitespace. For example:

```text
[    1.250000] APP_READY
```

After `]`, the parser consumes at most one separating space or tab. The remaining
text is the message, unchanged. A timestamp prefix on its own is a valid record
with an empty message.

Timestamps are converted to signed 64-bit integer nanoseconds using checked
integer arithmetic, never floating point. Overflow, signs, exponents, missing
digits, more than nine fractional digits, and malformed attempted prefixes are
invalid.

A line is treated as an attempted timestamp if it starts with `[` and the first
character after any ASCII spaces or tabs is an ASCII digit, `+`, `-`, or `.`. A malformed
prefix of this kind is an input error. Other lines, including `[ OK ] ready`,
remain messages without timestamps; their time is null. The parser never invents
a missing timestamp.

Timestamps must stay the same or increase across all timestamped records,
including the preamble before boot start. A decrease is invalid input and may
indicate a reboot or mixed clocks. The parser must not reorder records or reset
the timing origin automatically.

### Complete input and resource limits

The parser reads through the end of the file before checks can be reported.
Invalid UTF-8, reader errors other than EOF, malformed attempted timestamps, and
limit violations cause failure even if a ready marker has already appeared.
Empty input is valid to parse, but evaluation will report a missing boot start.
A parsing failure returns no usable partial `Log`.

The default limits are 16 MiB of input, 100,000 lines, and 64 KiB per line. Input
bytes include all line endings. Line length excludes LF or CRLF. Empty lines count
toward the line and input-byte limits. In the Go API, a zero option selects its
default and a negative option is invalid. CLI limit overrides must be strictly
positive.

The parser checks context cancellation between reads. It cannot interrupt an
arbitrary `io.Reader` already blocked in `Read`. The caller owns the reader and
is responsible for closing it.

## Rule files

A rule file contains exactly one JSON document, at most 1 MiB in size, with
`schema_version` set to 1:

```json
{
  "schema_version": 1,
  "boot_start": {"pattern": "^Linux version "},
  "milestones": [
    {"id": "network_ready", "pattern": "^NETWORK_READY$", "within_ms": 3000},
    {"id": "application_ready", "pattern": "^APP_READY$", "within_ms": 5000}
  ],
  "forbidden": [
    {"id": "kernel_panic", "pattern": "(?i)kernel panic"}
  ]
}
```

The parser rejects unknown fields, duplicate object keys at any level, trailing
JSON, incorrect types, invalid UTF-8 strings, duplicate IDs, unsupported versions,
overflowing numbers, and invalid patterns. Required fields must be present with
their declared types. Null is invalid for objects, strings, arrays, and integer
fields. Optional arrays may be omitted, which means they are empty; explicitly
supplying a null array is invalid.

The `boot_start` rule is mandatory, together with at least one milestone or
forbidden rule. Every configured milestone is required. Across the milestone and
forbidden arrays, there may be at most 128 rules, excluding `boot_start`. Reports
therefore contain at most 129 checks.

IDs must match `[A-Za-z][A-Za-z0-9_.-]{0,63}`, be unique across both arrays, and
must not equal the reserved ID `boot_start`. Patterns must be nonempty UTF-8
strings of at most 4,096 bytes and use Go's `regexp` syntax. Invalid expressions
and expressions that match the empty string are rejected.

`CompileRules` compiles each pattern once into a reusable ruleset; evaluation
reuses those expressions. Patterns match the message without the timestamp
prefix and are case-sensitive unless they explicitly change that behavior.
A record may match several rules. There is no glob syntax, matching across
records, backtracking engine, or automatic anchoring.

A milestone may omit `within_ms` to check only that its message appears. If
supplied, the deadline must be a nonnegative JSON integer that converts to int64
nanoseconds without overflow. A deadline of zero requires the first eligible
matching record to have exactly the boot-start timestamp. Equality with any
deadline passes. Deadlines use milliseconds; record and report timestamps use
integer nanoseconds.

## Boot start and evaluation

### Establishing the timing origin

The evaluator searches for the `boot_start` pattern throughout the log in file
order. Exactly one matching record with a timestamp establishes the origin: the
boot-start record and the starting point for elapsed-time calculations.

More than one boot-start match is invalid input, even if the clock never resets
or the repeated matches are identical banners. A single match without a timestamp
is also invalid input. Both conditions return an operational error and no report.

If boot start is absent, the report contains a failed `boot_start` check with
reason `missing`. Every milestone has status `not_evaluated` and reason
`no_boot_start`. Forbidden rules are still evaluated against the whole log.
A missing boot start is a failed check in a valid report, not a parsing error.

### Milestones

Milestones consider the boot-start record and all later records in file order.
Earlier matches cannot satisfy them. The boot-start record itself may satisfy a
milestone with elapsed time zero. The configuration controls report order, not
the order in which milestones must occur.

For each milestone, the first eligible matching record is kept as evidence. This
first record is also called the witness. Later matches contribute to the total
match count but never replace it. A record counts once per rule even if the
pattern matches several substrings in that record.

A milestone with no eligible match fails with `missing`. A milestone without a
deadline passes even if its first match has no timestamp. A milestone with a
deadline fails with `untimed` if its first match has no timestamp, even if a later
match does. Otherwise, its elapsed time is the first match's timestamp minus the
boot-start timestamp. It passes when that elapsed time is less than or equal to
the deadline converted to nanoseconds, and fails with `late` when it exceeds it.

### Forbidden messages and evidence

Forbidden rules inspect the entire supplied log, including the preamble,
messages without timestamps, and messages after readiness. Any matching record
fails the check with `forbidden_match`. If no record matches, the check passes. The report retains the
first matching record and the total match count. Input processing must continue
after a failure.

Each check retains at most one evidence record containing its original line
number, nullable `timestamp_ns`, and exact message. The full raw log is not
included by default, and repeated matches must not cause unbounded evidence to
accumulate.

When the origin and evidence both have timestamps, `elapsed_ns` is the evidence
timestamp minus the origin timestamp. It can be negative for forbidden evidence
before boot start and is zero for boot start itself. Otherwise it is null.
Milestone elapsed times cannot be negative.

### Overall result

Reports list `boot_start` first, then milestones in configuration order, then
forbidden rules in configuration order. Every configured check appears. Status
is `passed`, `failed`, or `not_evaluated`; the last is used only for milestones
when boot start is missing. Overall success means there are no failed checks.
A missing boot start always contributes a failed check.

The result describes only the supplied capture. Reaching the end of a file does
not prove that capture is complete. The result says nothing about omitted device
behavior, later failures outside the file, or the real-time accuracy of source
timestamps.

## Public Go API

The module is `github.com/firmdura/bootlog-check`, with root package `bootlog`.
The Go toolchain is pinned in `.go-version`, and `go.mod` declares the minimum
supported version. The implementation uses the standard library.

```go
ParseLog(ctx context.Context, r io.Reader, opts ParseOptions) (Log, error)
ParseRules(r io.Reader) (Config, error)
CompileRules(cfg Config) (*RuleSet, error)
Evaluate(log Log, rules *RuleSet) (Report, error)
```

The public types include `Record`, `Log`, `ParseOptions`, `Config` and its rule
types, `Report`, `CheckResult`, `Evidence`, and the error categories. The
[API and schema reference](schema-v1.md) defines their fields, JSON tags, enum
values, reason codes, limits, and complete runnable examples.

A `RuleSet` owns immutable compiled state and copies the caller's configuration.
Concurrent `Evaluate` calls may safely share it. `Evaluate` does not modify either
input, and a nil ruleset is an error.

The API validates configurations and logs built by callers as well as parsed
data. Callers cannot bypass requirements for line order, UTF-8, timestamp order
and range, unique IDs, valid options, or required fields. `Evaluate` checks a
log's structure without reapplying default parser limits, so custom parsing
limits remain effective. Callers who build logs directly are responsible for
their memory and input-size bounds.

Typed invalid-input, invalid-rules, limit, and cancellation errors support
`errors.Is` and `errors.As`, while preserving underlying I/O errors. The core
library does not print, exit the process, run host commands, access the network,
or use mutable global options. Evaluation is synchronous, accepts no context, and
has finite work bounded by the validated log and rule limits.

The public `report` package imports `bootlog` and provides
`Write(io.Writer, bootlog.Report, Format) error`. The core package never imports
`report`. The CLI uses `internal/cli` with injectable streams and a thin
`cmd/bootlog-check/main.go` entry point.

## CLI and output

```text
bootlog-check --rules FILE [flags] LOG
  --format text|json|junit      default text
  --max-input-bytes N
  --max-lines N
  --max-line-bytes N
  --help, -h
  --version
```

Flags must precede the single log path. Both inputs are local regular files;
stdin, URLs, compressed archives, and device ports are unsupported. Use shell
redirection to save reports. Limits use decimal counts or byte counts. `--help`
(or `-h`) and `--version` need no inputs and return exit code 0.

Since v1.1.0, `bootlog-check --check-rules FILE` validates the rule file
without a log. It takes no other flags and emits no report. Valid rules exit
0; an error exits 2. The table below applies when evaluating a capture.

| Exit code | Meaning |
| --- | --- |
| 0 | All checks passed. |
| 1 | A complete, valid report contains failed checks. |
| 2 | An argument, rules, log, origin, limit, cancellation, I/O, or rendering error prevented successful completion. |

Parsing, compilation, and evaluation errors produce no report bytes. Failed
checks still produce a complete report. A downstream write failure may leave
partial output and must return exit code 2. During a check, stdout contains only
the report; operational diagnostics go to stderr. Progress messages and raw logs must not
be printed to stdout.

Every format includes all checks, summary counts, timing origin and basis, first
evidence, match counts, and a statement of what a pass does and does not mean.
Reports contain no current date, host-processing duration, absolute host filename,
hostname, or other field that varies between runs.

Identical logical records and rules produce byte-identical output. LF and CRLF
versions of the same log produce the same report. Raw input-byte counts and line
ending style are excluded to preserve this equivalence. In JSON, empty lists are
`[]`, and absent timestamps, evidence, and origin are explicit `null` values.
Text and JUnit use their documented representations for absent values.

### Report formats

Text reports include a readable summary, each check's ID and reason, its deadline,
elapsed time, and first evidence with line number. They escape terminal control
characters and embedded line breaks. A failed report must not have a success
headline.

JSON reports contain the complete schema version 1 structure. Nanoseconds and
millisecond deadlines are integers. Standard JSON escaping preserves the
underlying valid Unicode and message data.

JUnit reports contain one `testsuite` named `bootlog-check` and one `testcase`
per check, including `boot_start`. Each testcase uses its stable check ID as its
name and `bootlog.start`, `bootlog.milestone`, or `bootlog.forbidden` as its
classname. Failed checks become `failure` elements; not-evaluated checks become
`skipped` elements. Suite counts must agree with those elements. Every produced
report has `errors=0` because operational errors do not produce JUnit reports.

JUnit output omits `time` and `timestamp` attributes so boot elapsed times cannot
be mistaken for test execution durations. Timing and first-evidence details
appear in failure or skipped text, or in `system-out`. XML metacharacters are
escaped. Characters forbidden in XML 1.0 appear as printable escapes rather than
raw bytes or silently deleted text. This is a documented, conservative JUnit
subset; compatibility with every importer is not guaranteed.

Report writers validate caller-built reports and return errors for invalid
formats or state, short writes, and writer failures. The caller owns the writer.
Matched messages appear in reports and require the same data handling as the
source log.

## Tests, demonstrations, and documentation

Each component includes its own tests. Default verification uses Go-generated
fixtures and independently defined expected results. It runs offline after Go
setup, without root, hardware, Docker, or QEMU. Tests cover complete-file
validation, all rule states, deadline boundaries, control characters, public
examples, and exit codes from the actual binary. Parser and rule fuzzing use
bounded inputs and limits.

The [offline replay](../examples/replay/README.md) checks synthetic logs against
expected results. The [QEMU example](../examples/qemu/README.md) includes six raw
serial captures from a pinned Linux kernel and initramfs booted under software
emulation. `/dev/kmsg` gives the guest markers kernel timestamps. A separate
checker reads the raw logs to verify the normal, missing, forbidden, and late
outcomes. The example includes reproduction commands and checksums. Kernel and
initramfs binaries are built locally.

## Primary references

- [Go regexp syntax and behavior](https://pkg.go.dev/regexp)
- [Linux serial console](https://www.kernel.org/doc/html/latest/admin-guide/serial-console.html)
- [QEMU direct Linux boot](https://www.qemu.org/docs/master/system/linuxboot.html)
- [QEMU invocation](https://www.qemu.org/docs/master/system/invocation.html)
- [Go encoding/xml](https://pkg.go.dev/encoding/xml)
