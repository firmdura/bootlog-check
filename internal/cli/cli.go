// Package cli composes the saved-log library and report writers for the command.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"syscall"

	"github.com/firmdura/bootlog-check"
	"github.com/firmdura/bootlog-check/report"
)

// Version is the command's release version.
const Version = "1.0.6"

// File is a caller-supplied input file. Run checks that Stat describes a regular
// file and closes each successfully opened input, including on errors.
type File interface {
	io.ReadCloser
	Stat() (os.FileInfo, error)
}

// Streams supplies output and optional input opening for an invocation. A nil
// Open uses local files. Injected openers must return a fresh owned File each
// time. Run never closes output streams. A nil Stderr discards diagnostics;
// a nil Stdout is an operational error.
type Streams struct {
	Stdout io.Writer
	Stderr io.Writer
	Open   func(string) (File, error)
}

const help = `Usage: bootlog-check --rules FILE [flags] LOG

Check one saved UTF-8 boot log against strict JSON rules. Inputs must be local
regular files; stdin, URLs, archives, and device ports are unsupported.
All flags precede the single log path. Use -- before a path beginning with '-'.

Flags:
  --rules FILE          Required strict JSON rule file (schema_version 1)
  --format FORMAT       text (default), json, or junit
  --max-input-bytes N   Raw log bytes, including terminators (default 16777216)
  --max-lines N         Physical log lines, including empty lines (default 100000)
  --max-line-bytes N    Bytes per line, excluding LF/CRLF (default 65536)
  -h, --help            Show this help without requiring input files
  --version             Show the command version without requiring input files

Limits must be positive decimal integers fitting signed 64-bit values.
The entire log is validated through EOF before a report is written.
Redirect stdout to save a report; operational diagnostics go to stderr.
Exit 0: checks pass (or help/version). Exit 1: complete report with failed checks.
Exit 2: invalid usage/input, limits, cancellation, or I/O/output failure.
Output failure can leave a partial report; discard that incomplete artifact.
A pass describes only the supplied capture, not capture completeness or device
health. Reports contain matched messages and follow the source log's data policy.
`

// Run executes one invocation, returning its process status (0, 1, or 2).
// It writes no report until rules, complete log input, owned-file closure, and
// evaluation succeed. Help/version use stdout; operational errors use stderr.
// Cancellation is checked between reads and phases. It cannot interrupt a read,
// open, synchronous evaluation, or output write already in progress.
func Run(ctx context.Context, args []string, streams Streams) int {
	stderr := streams.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "bootlog-check: %v\n", err)
		return 2
	}
	if streams.Stdout == nil {
		return fail(errors.New("stdout writer is required"))
	}
	var rulesPath, format string
	var showHelp, showVersion bool
	var opts bootlog.ParseOptions
	fs := flag.NewFlagSet("bootlog-check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&rulesPath, "rules", "", "rules file")
	fs.StringVar(&format, "format", string(report.Text), "report format")
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help")
	fs.BoolVar(&showVersion, "version", false, "show version")
	fs.Var(positiveLimit{&opts.MaxInputBytes}, "max-input-bytes", "positive decimal bytes")
	fs.Var(positiveLimit{&opts.MaxLines}, "max-lines", "positive decimal lines")
	fs.Var(positiveLimit{&opts.MaxLineBytes}, "max-line-bytes", "positive decimal bytes")
	if err := fs.Parse(args); err != nil {
		return fail(err)
	}
	if showHelp || showVersion {
		output := help
		if !showHelp {
			output = "bootlog-check " + Version + "\n"
		}
		if err := writeString(streams.Stdout, output); err != nil {
			return fail(fmt.Errorf("write command information: %w", err))
		}
		return 0
	}
	if rulesPath == "" || fs.NArg() != 1 {
		return fail(errors.New("require --rules FILE and exactly one LOG; flags must precede LOG (see --help)"))
	}
	if format != string(report.Text) && format != string(report.JSON) && format != string(report.JUnit) {
		return fail(fmt.Errorf("unsupported format %q (want text, json, or junit)", format))
	}
	if ctx == nil {
		return fail(errors.New("context is required"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	opener := streams.Open
	if opener == nil {
		opener = openLocal
	}
	ruleFile, err := openRegular(opener, rulesPath)
	if err != nil {
		return fail(fmt.Errorf("open rules: %w", err))
	}
	cfg, parseErr := bootlog.ParseRules(contextReader{ctx, ruleFile})
	if err := errors.Join(parseErr, ruleFile.Close()); err != nil {
		return fail(fmt.Errorf("read rules: %w", err))
	}
	rules, err := bootlog.CompileRules(cfg)
	if err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	logFile, err := openRegular(opener, fs.Arg(0))
	if err != nil {
		return fail(fmt.Errorf("open log: %w", err))
	}
	log, parseErr := bootlog.ParseLog(ctx, logFile, opts)
	if err := errors.Join(parseErr, logFile.Close()); err != nil {
		return fail(fmt.Errorf("read log: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	result, err := bootlog.Evaluate(log, rules)
	if err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := report.Write(contextWriter{ctx, streams.Stdout}, result, report.Format(format)); err != nil {
		return fail(err)
	}
	if result.Summary.Failed > 0 {
		return 1
	}
	return 0
}

type positiveLimit struct{ value *int64 }

func (v positiveLimit) String() string {
	if v.value == nil {
		return "0"
	}
	return strconv.FormatInt(*v.value, 10)
}
func (v positiveLimit) Set(s string) error {
	if s == "" {
		return errors.New("must be a positive decimal integer")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return errors.New("must contain decimal digits only")
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return errors.New("must be a positive decimal integer fitting int64")
	}
	*v.value = n
	return nil
}

func openLocal(path string) (File, error) {
	// Nonblocking open lets the subsequent regular-file check reject FIFOs
	// without waiting for another process. Supported hosts are Linux and macOS.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func openRegular(open func(string) (File, error), path string) (File, error) {
	if path == "-" {
		return nil, errors.New("stdin is unsupported; supply a local regular file")
	}
	file, err := open(path)
	if err != nil {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		return nil, err
	}
	if file == nil {
		return nil, errors.New("input opener returned no file")
	}
	info, err := file.Stat()
	if err == nil && (info == nil || !info.Mode().IsRegular()) {
		err = fmt.Errorf("%q is not a regular file", path)
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if canceled := r.ctx.Err(); canceled != nil {
		return n, canceled
	}
	return n, err
}

type contextWriter struct {
	ctx context.Context
	w   io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.w.Write(p)
	if err != nil {
		return n, err
	}
	return n, w.ctx.Err()
}

func writeString(w io.Writer, s string) error {
	n, err := io.WriteString(w, s)
	if err == nil && n != len(s) {
		err = io.ErrShortWrite
	}
	return err
}
