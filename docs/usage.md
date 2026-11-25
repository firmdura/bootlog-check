# Saved-log and CLI guide

This guide explains how to check a saved log with bootlog-check. Start with the
[quickstart](../README.md#quickstart) for a complete rules file and saved
capture.

## One saved capture

bootlog-check reads one saved boot log from a local UTF-8 text file. Timestamps
must come from the same clock and must never go backward. The tool uses those
timestamps as written; it does not measure time on the host or infer a timestamp
format. It does not capture or flash devices, separate multiple boots, diagnose
failures, or compare results with statistical baselines. You can check the log
on Linux or macOS regardless of the firmware's CPU architecture.

A passing report means only that the supplied capture meets the configured
checks. Reaching the end of the file does not prove that the capture is
complete. The report cannot account for failures after capture stopped, device
behavior that was never logged, or inaccurate timestamps. A matching ready
message does not establish that the device is healthy.

## Exact input grammar

A line has a timestamp only if it starts with `[` in the first column, followed
by a nonnegative decimal number of seconds and `]`. The number must use ASCII
digits. Spaces or tabs may follow `[`, but cannot appear between the number and
`]`. An optional fractional part must contain one through nine digits.

```text
[    0.000000] Linux version example
[1.250] APP_READY
[2] a whole-second timestamp
```

After `]`, the parser consumes at most one separating space or tab. All
remaining message bytes are preserved. `[2]` alone is a timed record with an
empty message. The parser converts times directly to signed 64-bit integer
nanoseconds, checking for overflow and using no floating-point arithmetic.
Signs, exponents, missing digits, overflow, or more than nine fractional digits
are invalid.

The parser treats a line as an attempted timestamp if it begins with `[` and the
first character after any spaces or tabs is an ASCII digit, `+`, `-`, or `.`. If
that prefix is malformed, parsing fails. For example, `[2.x] ready` is an error,
not an ordinary message. Other lines, including `[ OK ] ready`, are messages
without a timestamp, represented by `null`. A leading space before `[` also
makes a line untimed because the prefix is not at column one. The parser
preserves ANSI sequences and other valid UTF-8 control characters when matching
rules. Reports escape characters that would be unsafe to display directly.

The file may use Unix line endings (LF), Windows line endings (CRLF), or a final
line with no line ending. The parser removes only the line ending, including the
carriage return in CRLF. Empty lines do not become records, but still count
toward input limits. Lines containing only spaces or tabs remain messages.
Reported evidence uses line numbers from the original file, starting at 1, so
skipped empty lines leave gaps in those numbers.

Timestamps must never decrease, including in messages before boot start. Equal
timestamps are allowed. A decrease makes the input invalid and may indicate a
reboot, mixed clocks, or messages captured out of order. The checker does not
reorder messages or choose a new timing origin automatically. Invalid UTF-8,
malformed timestamp prefixes, read errors, and exceeded limits anywhere in the
file make it invalid, even after a ready message or a failed check. The parser
reads the whole file before evaluating rules. The [parser guide](parsing.md) has
additional exact-prefix, line-ending, and limit examples.

## Matching, origin, and elapsed time

Rules match only the message, never the removed timestamp prefix. Patterns use
[Go regular-expression syntax](https://pkg.go.dev/regexp/syntax). Matching is
case-sensitive and may find a match anywhere in the message unless the pattern
says otherwise. Use `^...$` for an exact message and `(?i)` for case-insensitive
matching. JSON requires its own backslash escaping, so the regex `\bready\b` is
written `"\\bready\\b"` in JSON. There are no globs, backreferences,
lookarounds, multiline record matching, or automatic stripping of ANSI control
sequences. One record may match many rules.

The required `boot_start` rule identifies the start of the boot. It must match
exactly one record in the entire log, and that record must have a timestamp.
That timestamp becomes the origin from which deadlines are measured. An
untimestamped start or multiple matching records makes the input invalid. If the
boot-start pattern matches repeated boot banners, the input is invalid even if
the clock never resets.

If no record matches, the report contains a failed `boot_start` check. Every
milestone has status `not_evaluated` and reason `no_boot_start`; forbidden rules
still run. An empty file is valid input and produces this missing-start result.

Milestones examine records at or after the start record in physical order.
Matches before the start record do not count. The start record itself may
satisfy a milestone with an elapsed time of zero. Milestones appear in the
report in the order you configured them, but they may occur in any order in the
log. Every configured milestone is required to appear.

The first eligible matching record supplies the evidence for a milestone. This
record is called the *witness*. Later matches increase `match_count` but do not
replace it. A record counts once per rule even if the pattern matches several
parts of the message. A milestone with no deadline can pass without a timestamp.
If a milestone has a deadline and its first match has no timestamp, it fails
with reason `untimed`, even if a later timestamped match would pass. A milestone
that never appears fails with reason `missing`.

Deadlines are integer milliseconds relative to the origin, not wall time or time
since another milestone. Elapsed report values are integer nanoseconds. A match
exactly at the deadline passes; a later first match fails with reason `late`.
Omit `within_ms` to require the message without imposing a deadline. Set it to
zero to require the first matching record to have exactly the same timestamp as
boot start. A first match without a timestamp still fails a zero-millisecond
deadline.

Forbidden rules cover the whole file, including messages before boot start,
messages without timestamps, and messages after readiness. Any match fails with
reason `forbidden_match`; no match passes with reason `absent`. The report keeps
the first matching record and the total number of matching records. Its elapsed
time can be negative if the match precedes boot start, or `null` if the origin
or matching record has no timestamp.

## Rules file

```json
{
  "schema_version": 1,
  "boot_start": {"pattern": "^Linux version "},
  "milestones": [
    {"id": "application_ready", "pattern": "^APP_READY$", "within_ms": 5000}
  ],
  "forbidden": [
    {"id": "kernel_panic", "pattern": "(?i)kernel panic"}
  ]
}
```

The rules file must contain exactly one JSON document, no larger than 1 MiB.
Version 1 and `boot_start.pattern` are required, along with at least one
milestone or forbidden rule. Optional arrays may be omitted; explicitly setting
them to `null` is invalid. Field names are exact and case-sensitive. Unknown
fields, duplicate keys at any depth, trailing documents, wrong types, invalid
UTF-8 strings, unpaired Unicode surrogate escapes, unsupported versions, and
overflowing numbers are rejected. See the [rule guide](rules.md) for the
implemented API and additional examples.

Each user rule requires `id` and `pattern`. IDs match
`[A-Za-z][A-Za-z0-9_.-]{0,63}`, are unique across both arrays, and cannot be
`boot_start`. At most 128 user rules are allowed, excluding boot start. Patterns
must contain 1 through 4,096 UTF-8 bytes, compile successfully, and not match
the empty string. A milestone's optional `within_ms` is an integer between zero
and 9,223,372,036,854 inclusive. Null, quoted numbers, fractional/exponential
numbers, and negative deadlines are invalid.

## CLI reference

```text
bootlog-check --rules FILE [flags] LOG
bootlog-check --check-rules FILE
```

Put all flags before the single log path. Both inputs must be local regular
files. The tool cannot read from standard input (`-`), URLs, compressed
archives, directories, or device ports. Use shell redirection to save reports.

Use `--check-rules FILE` on its own to check a rules edit before collecting a
log. It applies the same JSON, rule, size, and regular-expression checks as a
normal invocation. Valid rules produce no output and exit 0; errors go to
stderr and exit 2. Success says nothing about whether the rules match a boot.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--rules FILE` | Required for a log check | Strict version 1 JSON rule file |
| `--check-rules FILE` | | Validate a rules file without checking a log |
| `--format text\|json\|junit` | `text` | Report encoding |
| `--max-input-bytes N` | `16777216` | Maximum raw log bytes, including terminators |
| `--max-lines N` | `100000` | Maximum physical lines, including empty ones |
| `--max-line-bytes N` | `65536` | Maximum bytes per line, excluding LF/CRLF |
| `--help`, `-h` | | Usage; no input required; exits 0 |
| `--version` | | Version; no input required; exits 0 |

Limit flags accept only positive decimal integers. A zero value selects a
default in the Go library, but is invalid on the command line. The rules-file
size and rule-count limits cannot be changed. The parser keeps accepted records
in memory, so larger logs use more memory even though input sizes are capped.

| Exit | Meaning | Output |
| --- | --- | --- |
| `0` | All checks pass | Complete report on stdout |
| `1` | Valid evaluation contains failed checks | Complete report on stdout |
| `2` | Arguments, input, rules, limits, cancellation, I/O, or rendering failed | Diagnostic on stderr; pre-render errors emit no report |

During a normal check, stdout contains only the report; no progress messages or
raw log dump are printed. Help and version requests write their requested text
there instead. If writing the output fails, the command returns 2 and may leave
a partial report. A redirected file's existence does not establish that it is
complete. If the shell cannot open a redirected file, the checker never runs;
that failure comes from the shell and does not have a checker exit code.

## Output and data handling

Every report includes the boot-start check, followed by milestones in configured
order, then forbidden rules in configured order. Reports include summary and
match counts, the timing origin and basis, the first matching record for each
check, deadlines, elapsed times, and an explanation of what a pass means.
Repeated runs produce identical reports, including when equivalent input uses LF
or CRLF line endings. Every format ends with a newline. Reports exclude host
paths, hostnames, the current date, processing duration, raw input byte counts,
and line-ending style.

JSON uses `schema_version: 1`. It emits empty arrays as `[]` and absent
evidence, origin, or times as explicit `null`. Nanoseconds and milliseconds are
integers; consumers must retain int64 precision without a floating-point round
trip. JSON uses two-space indentation and standard `encoding/json` escaping,
retaining underlying Unicode and control data when decoded. The [schema
reference](schema-v1.md) defines every field and valid reason. Consumers should
check the version instead of assuming a future version has the same structure.

Text begins with `bootlog-check: PASS` or `bootlog-check: FAIL`. Absent numbers,
origin, and evidence are written as `none`; a real zero remains `0`. Messages
use double-quoted Go string syntax: controls and embedded line breaks become
printable escapes such as `\n`, `\t`, `\x1b`, and `\x00`, while printable
Unicode stays readable. Literal backslashes are escaped too, so an actual
newline (`\n`) remains distinct from a backslash followed by `n` (`\\n`).
`strconv.Unquote` reverses that message representation.

JUnit output contains one `testsuite` named `bootlog-check` and one `testcase`
per check. Testcase names are stable rule IDs and classnames are
`bootlog.start`, `bootlog.milestone`, and `bootlog.forbidden`. Failed checks map
to `failure`; not-evaluated milestones map to `skipped`. Suite counts match the
children and `errors` is zero, because operational errors do not produce a JUnit
report. There are no `time` or `timestamp` attributes: boot elapsed times are
not test execution durations. Every testcase has `system-out` with check
details, including passing cases. Failure/skipped text repeats the relevant
details. The suite's `system-out` contains summary, schema, origin, timing
basis, and scope. These details use the same `none` and reversible
quoted-message conventions as text, followed by XML metacharacter escaping. XML
1.0-forbidden characters such as NUL, U+FFFE, and U+FFFF become printable
escapes rather than being removed. After XML decoding, `strconv.Unquote`
recovers a quoted message. Some JUnit importers may not support this subset of
the format. See the [report guide and examples](../report/README.md) for the
exact output structure.

Reports contain exact matched messages, not the whole input. They can still
contain sensitive source-log data. Apply the log's access and retention policy
to reports and uploaded artifacts. See [CI usage](ci.md) for preserving exit
codes while collecting output.
