# Go library

Use the Go library to read a saved log, apply rules, and write a report from
your own application. The [complete example](../examples/library/main.go) uses
only the public packages. The [tested
examples](../examples/library/example_test.go) also show how to handle typed
errors and output failures.

Import the root package as `bootlog` and the output package as `report`:

```go
import (
    "github.com/firmdura/bootlog-check"
    "github.com/firmdura/bootlog-check/report"
)
```

The module is public and can be downloaded with the standard Go tools. No
GitHub credentials or private-module configuration are needed. See the
[installation guide](../README.md#installation) for Go version requirements.
API documentation is available on pkg.go.dev for
[bootlog](https://pkg.go.dev/github.com/firmdura/bootlog-check) and
[report](https://pkg.go.dev/github.com/firmdura/bootlog-check/report).

## Run the examples from a consumer module

From the repository root, run the example and the tests that exercise its error
handling:

```sh
go run ./examples/library
go test ./examples/library
```

To exercise the same program outside the repository, start in the checkout:

```sh
bootlog_source=$PWD
bootlog_consumer=$(mktemp -d)
cp "$bootlog_source/examples/library/main.go" "$bootlog_consumer/main.go"
cd "$bootlog_consumer"
go mod init example.com/bootlog-consumer
go get github.com/firmdura/bootlog-check@v1.0.8
go run .
cd "$bootlog_source"
```

These commands download the module and run the program, which prints a passing
text report for a synthetic log. The ready message appears exactly at its
deadline. `@v1.0.8` pins this release in `go.mod`; keep the consumer project's
module files so the example can be reproduced. Downloading requires network
access. Once Go and the module are installed, checks can run offline.

The copied program shows how to combine `ParseRules`, `CompileRules`,
`ParseLog`, `Evaluate`, and `report.Write`. The [error-handling
examples](../examples/library/example_test.go) cover `errors.Is`, `errors.As`,
cancellation, failed checks, and the original cause of a write error.

## Parse, compile, evaluate, render

The four main functions are:

```go
ParseLog(context.Context, io.Reader, ParseOptions) (Log, error)
ParseRules(io.Reader) (Config, error)
CompileRules(Config) (*RuleSet, error)
Evaluate(Log, *RuleSet) (Report, error)
```

`ParseRules` validates one strict JSON rules document. Use it instead of
decoding rule files with `encoding/json.Unmarshal`, which does not enforce this
format's duplicate-key, required-field, null, and unknown-field restrictions.
`CompileRules` validates the configuration and compiles its regular expressions
once. You can pass a `Config` returned by `ParseRules` or one you built in Go.
There is no need to call `Config.Validate` first.

`ParseLog` requires a nonnil context and reader. It reads the input into a
`Log`, subject to the configured limits. On failure it returns a zero `Log`; you
cannot use a partial parse result.

`Evaluate` validates the log, checks every configured rule, and returns a
complete `Report`. Check the returned error first, then test
`result.Summary.Failed == 0` to determine whether all checks passed. A missing
milestone, forbidden match, or missing boot-start message is a failed check
recorded in the report. Multiple boot-start matches or a start with no timestamp
make the input invalid and return a Go error without a report.

To write the report, call `report.Write(writer, result, format)` with
`report.Text`, `report.JSON`, or `report.JUnit`. The function validates the
format and report before writing any bytes; an empty format or inconsistent
report returns an error. It also rejects a nil writer interface.

Always check the write error. If the writer returns an error, `report.Write`
preserves it for `errors.Is` and `errors.As`, even when the write is incomplete.
An incomplete write with no accompanying writer error returns `io.ErrShortWrite`.
A write error may leave partial output, so treat the destination as a complete
report only after the call succeeds. Marshaling a report directly with `encoding/json` does
not perform the validation that `report.Write` provides.

## Ownership and concurrency

Readers and writers belong to the caller. The library does not open or close
them. Callers opening files must close them, and callers buffering output must
flush the buffer and check the flush error too. Library operations do not print,
exit the process, execute input, access the network, or retain global mutable
options.

`CompileRules` copies configuration, including slices and deadline pointers. The
resulting `RuleSet` has no mutable public fields and can be shared by concurrent
`Evaluate` calls. A nil or zero-value ruleset is invalid. Recompile to change
rules. `Evaluate` changes neither the log nor the ruleset. It copies the
evidence and deadlines into the returned report, so those values do not share
mutable storage with the inputs. Do not change a log or configuration while
another goroutine is reading it. You are responsible for synchronizing access to
shared readers, writers, and other mutable values you supply.

`report.Write` does not change the report. Several goroutines may render the
same report as long as no one changes it or its referenced data during writing.
If the goroutines share an output writer, synchronize access to that writer.

`Log`, `Config`, and `Report` are public models. Their validation methods check
values you construct in Go, as well as parsed values. The [schema
reference](schema-v1.md) lists fields, null semantics, status/reason values, and
consistency requirements. A nil timestamp means unknown time; a pointer to zero
means a known timestamp of zero. A nil deadline requires only that the milestone
appear. A pointer to zero requires the first eligible match to have exactly the
boot-start timestamp.

## Memory and cancellation

The parser keeps the accepted text and records in memory. Input limits bound
that memory use, but larger logs still require more memory. `ParseOptions{}`
defaults to 16 MiB input, 100,000 physical lines, and 64 KiB per physical line.
Each zero field selects its default independently; negative fields are invalid.
If a trusted log exceeds a limit, raise only the limit it needs. The input-byte
cap includes line terminators, and the line count includes empty lines. Per-line
bytes exclude LF or CRLF.

`Evaluate` calls `Log.Validate`; calling it yourself immediately beforehand
scans the same records twice. Validation formats field names such as
`records[42].message` only when it finds an error. It does not reapply the
default parsing limits, so a log parsed with higher limits remains valid.
If you build a `Log` directly, you are responsible for bounding its size and
memory use. Rules are capped at 128 user rules and 4,096 UTF-8 bytes per
expression; parsed rule documents are capped at 1 MiB.

`ParseLog` checks for context cancellation between reads. If an `io.Reader` is
already blocked in `Read`, canceling the context cannot interrupt it. Use the
reader's own interruption mechanism when that is needed. Evaluation runs
synchronously and finishes within the work determined by the validated log and
rule limits. `Evaluate`, `ParseRules`, and `report.Write` do not accept a
context.

## Error handling

Use `errors.Is` for categories and `errors.As` for the exported pointer types.
Do not branch on human-readable error text.

| Category | Concrete type | Meaning |
| --- | --- | --- |
| `bootlog.ErrInvalidInput` | `*bootlog.InvalidInputError` | Bad options, malformed log/model, log-reader failure, duplicate or untimed start |
| `bootlog.ErrInvalidRules` | `*bootlog.InvalidRulesError` | Invalid rule JSON, configuration, expression, rule-reader failure, or nil/zero ruleset |
| `bootlog.ErrLimit` | `*bootlog.LimitError` | Input, line, or rules-document cap exceeded |
| `bootlog.ErrCanceled` | `*bootlog.CancellationError` | Parsing canceled |
| `bootlog.ErrInvalidReport` | `*bootlog.InvalidReportError` | Caller-built report has inconsistent state |

Wrapped I/O and context errors retain their identity. For example,
`errors.Is(err, context.Canceled)` can identify the underlying cancellation; the
category is `bootlog.ErrCanceled`. Limit and cancellation errors are distinct
categories, not automatically invalid-input errors. Writer errors must also be
handled even when none of these categories match.

The evidence for each check contains its first matching message, exactly as it
appeared in the source log. Reports can contain identifiers, credentials, or
other sensitive log data even though they do not contain the whole log. Apply
the source log's retention and access policy to reports and CI artifacts.

For more examples using the public API, see the [parser
examples](../parser_example_test.go) and [rule and evaluation
examples](../rules_examples_test.go). See the component guides for [parser
details](parsing.md) and [rule/evaluation details](rules.md). The [report
example](../report/example_test.go) parses, evaluates, and renders a capture;
the [report guide](../report/README.md) defines exact output conventions.
