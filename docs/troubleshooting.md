# Troubleshooting

Start with the exit code and the messages on stderr. Exit code 1 means the
checker produced a complete report containing failed checks. Exit code 2 means
it could not complete the check or write the report. A write failure may leave
partial output, so keep the error messages alongside any saved reports.

## A clone or module download fails

This repository and its Go module are public, so downloads do not need GitHub
credentials. Check your network access and the repository URL or module version
in the command. If you previously configured this module as private, remove its
entry from `GOPRIVATE` to use Go's normal public module proxy and checksum checks.
Preserve unrelated entries. A custom proxy, Git URL rewrite, or credential helper
can also affect downloads. See the
[installation instructions](../README.md#installation).

## Go reports an unsupported version

Install the Go version pinned in `.go-version`. Routine verification uses
`GOTOOLCHAIN=local` and disables downloads, so it cannot install the required Go
version for you.

## A timestamp prefix is malformed

Inspect the line number in the error message. A timestamp must start with `[` in
the first column, followed by optional spaces or tabs, a nonnegative decimal
number of seconds, and `]`. A fractional part may have one through nine digits.
Signs, exponents, missing digits, and spaces between the number and `]` are
invalid. For example, `[1.250] READY` is valid, but `[1.250 ] READY` is not.

Compare the saved format with the [input grammar](usage.md#exact-input-grammar).
Preserve the original capture; do not invent timestamps to make a check pass.

## Timestamps go backward

Compare the two lines named in the error. For example, `invalid input at line 6
(timestamp_ns): must be nondecreasing; previous timestamp at line 3` means
line 6 has an earlier timestamp than line 3. Blank or untimed lines between
them still count toward the line numbers.

Check for concatenated boots, a reboot, mixed clocks, or messages captured out
of order. v1.0 expects a single boot with timestamps from one clock. It does not
reorder messages or split a file into separate boots.

## The boot-start check fails or returns an error

If `boot_start` is missing, the command exits with code 1. Check that its
pattern matches the message without the timestamp prefix and that the capture
includes the start of the boot. Milestones cannot be evaluated without a
boot-start origin, but forbidden rules still inspect the whole file.

If the start has no timestamp or the pattern matches more than once, the command
exits with code 2. For duplicate matches, the diagnostic identifies the second
matching line and gives the physical line number of the first. Compare those
two lines in the saved capture: a broad pattern can match two different
messages, or a repeated boot banner can match twice even when the clock never
resets. The start pattern must match exactly one record, and that record must
have a timestamp.

## A ready message is present, but the milestone fails

Only matches at or after the boot-start record count. The first eligible match
determines the result; later matches add to the count but do not replace it. If
that first match has no timestamp, a milestone with a deadline fails with reason
`untimed`. If it is too late, the reason is `late`.

Check the pattern's case, anchors, and JSON backslash escaping. ANSI control
sequences remain in the message during matching and can affect an exact match.
Deadlines are in milliseconds relative to the boot-start timestamp. See
[matching and elapsed time](usage.md#matching-origin-and-elapsed-time).

## A forbidden message appears after readiness

This is still a failed check. Forbidden rules inspect the entire file, including
messages before boot start, messages without timestamps, and messages after
readiness. Input validation also continues to the end of the file.

## The rules file or a pattern is rejected

Check `schema_version`, required fields, unique rule IDs, and JSON value types.
Unknown fields, duplicate keys, explicit `null` values, and trailing JSON are
invalid. Patterns use Go regular-expression syntax and must not match an empty
string. Backreferences and lookarounds are unsupported. The [rule
guide](rules.md) explains these restrictions with examples.

## An input or line limit is exceeded

For `max_line_bytes`, inspect the reported line in the original capture. Line
numbers start at 1 and count empty lines. The per-line byte limit includes the
timestamp prefix but not the LF or CRLF ending. A lone CR at the end of the
file counts as data. The input-byte limit includes all line endings.
For a trusted capture, raise only the limit you need. CLI
limit flags accept positive decimal integers. Rules-file size and rule-count
limits are fixed. See the [CLI reference](usage.md#cli-reference).

## The input contains invalid UTF-8

Keep the original capture and inspect its encoding or look for corruption.
bootlog-check accepts UTF-8 only and does not replace invalid bytes silently.

## Library parsing is canceled or stuck in a read

`ParseLog` checks the context between reads. Canceling the context cannot
interrupt a `Read` call that is already blocked; use the reader's own
interruption mechanism. Evaluation runs synchronously and does not accept a
context. See [memory and cancellation](library.md#memory-and-cancellation).

## A report is empty or truncated

Check stderr and the exit code. Confirm that the output destination is writable
and has space. A closed pipe or another write failure is an operational error
and may leave a partial report. Shell redirection can also fail before the
program starts. Discard incomplete output rather than treating it as a result.

## CI passes despite failed checks

Make sure CI uses the executable's exit code. `|| true`, an unchecked pipeline,
or a later successful command can hide a failure. Saving reports as artifacts
does not interpret their test results or fail a job by itself. The [CI
guide](ci.md) shows how to preserve failures while uploading reports.

The [usage guide](usage.md), [library guide](library.md), and [schema
reference](schema-v1.md) describe the supported behavior in detail. A ready
message or passing report cannot establish that the device is healthy or that
the capture is complete.
