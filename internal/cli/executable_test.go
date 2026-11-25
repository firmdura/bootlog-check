package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	bootlog "github.com/firmdura/bootlog-check"
	"github.com/firmdura/bootlog-check/report"
)

const executableRules = `{"schema_version":1,"boot_start":{"pattern":"^START$"},"milestones":[{"id":"ready","pattern":"^READY$","within_ms":1000}],"forbidden":[{"id":"panic","pattern":"PANIC"}]}`

// This builds and launches the shipped main, so os.Exit, stream wiring, and
// signal behavior are exercised independently of Run's return value.
func TestExecutable(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "bootlog-check")
	build := exec.Command("go", "build", "-o", binary, "./cmd/bootlog-check")
	build.Dir = root
	build.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build executable: %v\n%s", err, out)
	}

	t.Run("report-states", func(t *testing.T) { testExecutableReports(t, binary) })
	t.Run("operational-errors", func(t *testing.T) { testExecutableErrors(t, binary) })
	t.Run("help-version", func(t *testing.T) { testExecutableInformation(t, binary) })
	t.Run("check-rules", func(t *testing.T) {
		rules, log := executableFiles(t, executableRules, "[0] START\n")
		code, stdout, stderr := executableRun(t, binary, "--check-rules", rules)
		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("valid rules: code=%d stdout=%q stderr=%q; want silent success", code, stdout, stderr)
		}
		bad, _ := executableFiles(t, strings.Replace(executableRules, "^READY$", "[", 1), "")
		code, stdout, stderr = executableRun(t, binary, "--check-rules", bad)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "milestones[0].pattern") {
			t.Fatalf("invalid pattern: code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		for _, args := range [][]string{
			{"--check-rules"}, {"--check-rules", "-"},
			{"--check-rules", rules, log}, {"--rules", rules, "--check-rules", rules},
			{"--version", "--check-rules", bad},
			{"--check-rules", rules, "--format", "json"}, {"--check-rules", rules, "--max-lines", "10"},
			{"--check-rules", bad, "--check-rules", rules},
		} {
			assertExecutableError(t, binary, args...)
		}
	})
	t.Run("equivalence-and-composition", func(t *testing.T) { testExecutableEquivalence(t, binary) })
	t.Run("local-paths-and-limits", func(t *testing.T) { testExecutableLocalPaths(t, binary) })
	t.Run("broken-output-pipe", func(t *testing.T) {
		rules, log := executableFiles(t, executableRules, "[0] START\n[1] READY\n")
		readEnd, writeEnd, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := readEnd.Close(); err != nil {
			t.Fatal(err)
		}
		defer writeEnd.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "--rules", rules, "--format", "json", log)
		cmd.Stdout = writeEnd
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err = cmd.Run()
		if ctx.Err() != nil {
			t.Fatal("executable blocked writing to a closed pipe")
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || stderr.Len() == 0 {
			t.Fatalf("broken pipe: err=%v, stderr=%q; want exit 2 and diagnostic", err, stderr.String())
		}
	})
}

func testExecutableReports(t *testing.T, binary string) {
	zero, second := int64(0), int64(1_000_000_000)
	start := executableEvidence(1, &zero, "START")
	ready := executableEvidence(2, &second, "READY")
	startPassed := executableCheck("boot_start", bootlog.KindStart, bootlog.StatusPassed, bootlog.ReasonMatched, nil, &zero, 1, start)
	readyPassed := executableCheck("ready", bootlog.KindMilestone, bootlog.StatusPassed, bootlog.ReasonMatched, executableInt(1000), &second, 1, ready)
	panicAbsent := executableCheck("panic", bootlog.KindForbidden, bootlog.StatusPassed, bootlog.ReasonAbsent, nil, nil, 0, nil)
	readyMissing := executableCheck("ready", bootlog.KindMilestone, bootlog.StatusFailed, bootlog.ReasonMissing, executableInt(1000), nil, 0, nil)
	startMissing := executableCheck("boot_start", bootlog.KindStart, bootlog.StatusFailed, bootlog.ReasonMissing, nil, nil, 0, nil)
	readySkipped := executableCheck("ready", bootlog.KindMilestone, bootlog.StatusNotEvaluated, bootlog.ReasonNoBootStart, executableInt(1000), nil, 0, nil)
	cases := []struct {
		name, log string
		code      int
		origin    *bootlog.Evidence
		checks    []bootlog.CheckResult
		summary   bootlog.Summary
	}{
		{"inclusive-deadline-pass", "[0] START\n[1] READY\n", 0, start, []bootlog.CheckResult{startPassed, readyPassed, panicAbsent}, bootlog.Summary{Total: 3, Passed: 3}},
		{"missing-milestone", "[0] START\n", 1, start, []bootlog.CheckResult{startPassed, readyMissing, panicAbsent}, bootlog.Summary{Total: 3, Passed: 2, Failed: 1}},
		{"late-first-witness", "[0] START\n[1.001] READY\n[1.002] READY\n", 1, start, []bootlog.CheckResult{startPassed, executableCheck("ready", bootlog.KindMilestone, bootlog.StatusFailed, bootlog.ReasonLate, executableInt(1000), executableInt(1_001_000_000), 2, executableEvidence(2, executableInt(1_001_000_000), "READY")), panicAbsent}, bootlog.Summary{Total: 3, Passed: 2, Failed: 1}},
		{"untimed-first-witness", "[0] START\nREADY\n[1] READY\n", 1, start, []bootlog.CheckResult{startPassed, executableCheck("ready", bootlog.KindMilestone, bootlog.StatusFailed, bootlog.ReasonUntimed, executableInt(1000), nil, 2, executableEvidence(2, nil, "READY")), panicAbsent}, bootlog.Summary{Total: 3, Passed: 2, Failed: 1}},
		{"missing-origin", "[1] READY\nPANIC\n", 1, nil, []bootlog.CheckResult{startMissing, readySkipped, executableCheck("panic", bootlog.KindForbidden, bootlog.StatusFailed, bootlog.ReasonForbiddenMatch, nil, nil, 1, executableEvidence(2, nil, "PANIC"))}, bootlog.Summary{Total: 3, Failed: 2, NotEvaluated: 1}},
		{"empty-log", "", 1, nil, []bootlog.CheckResult{startMissing, readySkipped, panicAbsent}, bootlog.Summary{Total: 3, Passed: 1, Failed: 1, NotEvaluated: 1}},
		{"pre-origin-milestone", "[0] READY\n[1] START\n", 1, executableEvidence(2, &second, "START"), []bootlog.CheckResult{executableCheck("boot_start", bootlog.KindStart, bootlog.StatusPassed, bootlog.ReasonMatched, nil, &zero, 1, executableEvidence(2, &second, "START")), readyMissing, panicAbsent}, bootlog.Summary{Total: 3, Passed: 2, Failed: 1}},
		{"forbidden-before-origin", "[0] PANIC\n[1] START\n[2] READY\n", 1, executableEvidence(2, &second, "START"), []bootlog.CheckResult{executableCheck("boot_start", bootlog.KindStart, bootlog.StatusPassed, bootlog.ReasonMatched, nil, &zero, 1, executableEvidence(2, &second, "START")), executableCheck("ready", bootlog.KindMilestone, bootlog.StatusPassed, bootlog.ReasonMatched, executableInt(1000), &second, 1, executableEvidence(3, executableInt(2_000_000_000), "READY")), executableCheck("panic", bootlog.KindForbidden, bootlog.StatusFailed, bootlog.ReasonForbiddenMatch, nil, executableInt(-1_000_000_000), 1, executableEvidence(1, &zero, "PANIC"))}, bootlog.Summary{Total: 3, Passed: 2, Failed: 1}},
		{"forbidden-after-ready", "[0] START\n[1] READY\n[2] PANIC\x00<&>\n", 1, start, []bootlog.CheckResult{startPassed, readyPassed, executableCheck("panic", bootlog.KindForbidden, bootlog.StatusFailed, bootlog.ReasonForbiddenMatch, nil, executableInt(2_000_000_000), 1, executableEvidence(3, executableInt(2_000_000_000), "PANIC\x00<&>"))}, bootlog.Summary{Total: 3, Passed: 2, Failed: 1}},
		{"repeat-count-first-evidence", "[0] START\n[1] READY\n[2] READY", 0, start, []bootlog.CheckResult{startPassed, executableCheck("ready", bootlog.KindMilestone, bootlog.StatusPassed, bootlog.ReasonMatched, executableInt(1000), &second, 2, ready), panicAbsent}, bootlog.Summary{Total: 3, Passed: 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules, log := executableFiles(t, executableRules, tc.log)
			want := bootlog.Report{SchemaVersion: 1, TimingBasis: "boot_start", Origin: tc.origin, Checks: tc.checks, Summary: tc.summary, Scope: bootlog.CaptureScope}
			for _, format := range []string{"text", "json", "junit"} {
				t.Run(format, func(t *testing.T) {
					code, stdout, stderr := executableRun(t, binary, "--rules", rules, "--format", format, log)
					if code != tc.code || stderr != "" || stdout == "" {
						t.Fatalf("code=%d stdout=%q stderr=%q; want code=%d complete report and no stderr", code, stdout, stderr, tc.code)
					}
					assertExecutableReport(t, format, stdout, want)
					if strings.Contains(stdout, log) || strings.Contains(stdout, rules) {
						t.Fatal("report leaked a host input path")
					}
				})
			}
		})
	}
}

func testExecutableErrors(t *testing.T, binary string) {
	valid := "[0] START\n[1] READY\n"
	cases := []struct {
		name, rules, log string
		flags            []string
	}{
		{"duplicate-origin", executableRules, valid + "[2] START\n", nil},
		{"untimed-origin", executableRules, "START\n[1] READY\n", nil},
		{"clock-reset", executableRules, valid + "[0] reset\n", nil},
		{"malformed-trailing-timestamp", executableRules, valid + "[1.2.3] trailing\n", nil},
		{"invalid-utf8-log", executableRules, valid + string([]byte{0xff}), nil},
		{"invalid-json", "{", valid, nil},
		{"invalid-rules-version", strings.Replace(executableRules, `"schema_version":1`, `"schema_version":2`, 1), valid, nil},
		{"duplicate-rule-key", strings.Replace(executableRules, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1), valid, nil},
		{"unknown-rule-key", strings.Replace(executableRules, `"schema_version":1`, `"unknown":1,"schema_version":1`, 1), valid, nil},
		{"invalid-regex", strings.Replace(executableRules, `^READY$`, `[`, 1), valid, nil},
		{"invalid-utf8-rules", executableRules + string([]byte{0xff}), valid, nil},
		{"oversized-rules", strings.Repeat(" ", int(bootlog.MaxRulesBytes)+1), valid, nil},
		{"gzip-input", executableRules, string([]byte{0x1f, 0x8b, 0x08, 0xff}), nil},
		{"input-byte-limit", executableRules, valid, []string{"--max-input-bytes", "1"}},
		{"physical-line-limit", executableRules, valid, []string{"--max-lines", "1"}},
		{"line-byte-limit", executableRules, valid, []string{"--max-line-bytes", "1"}},
		{"limit-after-ready", executableRules, valid + "extra\n", []string{"--max-input-bytes", strconv.Itoa(len(valid))}},
		{"invalid-format", executableRules, valid, []string{"--format", "yaml"}},
		{"unknown-flag", executableRules, valid, []string{"--wat"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules, log := executableFiles(t, tc.rules, tc.log)
			args := append([]string{"--rules", rules}, tc.flags...)
			assertExecutableError(t, binary, append(args, log)...)
		})
	}
	for _, flag := range []string{"--max-input-bytes", "--max-lines", "--max-line-bytes"} {
		for _, value := range []string{"0", "-1", "+1", "1.0", "1e3", "0x10", " 1", "1 ", "", "9223372036854775808"} {
			t.Run(flag+"="+value, func(t *testing.T) {
				rules, log := executableFiles(t, executableRules, valid)
				assertExecutableError(t, binary, "--rules", rules, flag+"="+value, log)
			})
		}
	}
	rules, log := executableFiles(t, executableRules, valid)
	missing := filepath.Join(t.TempDir(), "does not exist")
	for name, args := range map[string][]string{
		"no-arguments":           nil,
		"missing-rules":          {log},
		"missing-rules-value":    {"--rules"},
		"missing-log":            {"--rules", rules},
		"extra-log":              {"--rules", rules, log, log},
		"flag-after-log":         {"--rules", rules, log, "--format", "json"},
		"missing-format-value":   {"--rules", rules, "--format"},
		"absent-log-file":        {"--rules", rules, missing},
		"absent-rules-file":      {"--rules", missing, log},
		"log-directory":          {"--rules", rules, filepath.Dir(log)},
		"rules-directory":        {"--rules", filepath.Dir(rules), log},
		"log-device":             {"--rules", rules, "/dev/null"},
		"rules-device":           {"--rules", "/dev/null", log},
		"stdin-log":              {"--rules", rules, "-"},
		"stdin-rules":            {"--rules", "-", log},
		"url-log":                {"--rules", rules, "https://example.invalid/boot.log"},
		"url-rules":              {"--rules", "https://example.invalid/rules.json", log},
		"unknown-flag-with-help": {"--unknown", "--help"},
	} {
		t.Run(name, func(t *testing.T) { assertExecutableError(t, binary, args...) })
	}
	t.Run("fifo-inputs-do-not-block", func(t *testing.T) {
		fifo := filepath.Join(t.TempDir(), "capture pipe")
		if err := syscall.Mkfifo(fifo, 0600); err != nil {
			t.Fatal(err)
		}
		// No process opens the write end. Opening before rejecting the file
		// type would block, so the subprocess timeout makes that regression
		// fail instead of hanging the test run indefinitely.
		assertExecutableError(t, binary, "--rules", rules, fifo)
		assertExecutableError(t, binary, "--rules", fifo, log)
	})
}

func testExecutableInformation(t *testing.T, binary string) {
	for _, flag := range []string{"--help", "-h", "--version"} {
		t.Run(flag, func(t *testing.T) {
			code, stdout, stderr := executableRun(t, binary, flag)
			if code != 0 || stdout == "" || stderr != "" {
				t.Fatalf("code=%d stdout=%q stderr=%q; want informational success", code, stdout, stderr)
			}
			if flag != "--version" {
				for _, phrase := range []string{"--rules", "--check-rules", "--format", "--max-input-bytes", "--max-lines", "--max-line-bytes", "--help", "--version", "text", "json", "junit"} {
					if !strings.Contains(stdout, phrase) {
						t.Errorf("help omits %q", phrase)
					}
				}
			} else if !strings.Contains(stdout, "bootlog-check") {
				t.Errorf("version does not identify executable: %q", stdout)
			}
		})
	}
}

func testExecutableEquivalence(t *testing.T, binary string) {
	text := "preamble\n[0] START\n[1] READY\n[2] READY\n[3] PANIC\n"
	rulesPath, lf := executableFiles(t, executableRules, text)
	_, crlf := executableFiles(t, executableRules, strings.ReplaceAll(text, "\n", "\r\n"))
	cfg, err := bootlog.ParseRules(strings.NewReader(executableRules))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := bootlog.CompileRules(cfg)
	if err != nil {
		t.Fatal(err)
	}
	log, err := bootlog.ParseLog(context.Background(), strings.NewReader(text), bootlog.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := bootlog.Evaluate(log, rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []report.Format{report.Text, report.JSON, report.JUnit} {
		t.Run(string(format), func(t *testing.T) {
			var fromLibrary bytes.Buffer
			if err := report.Write(&fromLibrary, result, format); err != nil {
				t.Fatal(err)
			}
			var first string
			for i, input := range []string{lf, lf, crlf} {
				code, stdout, stderr := executableRun(t, binary, "--rules", rulesPath, "--format", string(format), input)
				if code != 1 || stderr != "" {
					t.Fatalf("run %d: code=%d stderr=%q", i, code, stderr)
				}
				if i == 0 {
					first = stdout
				}
				if stdout != first || stdout != fromLibrary.String() {
					t.Fatalf("run %d: CLI, repeated replay, LF/CRLF, and composed library differ", i)
				}
			}
		})
	}
	code, stdout, stderr := executableRun(t, binary, "--rules", rulesPath, lf)
	var expectedText bytes.Buffer
	if err := report.Write(&expectedText, result, report.Text); err != nil {
		t.Fatal(err)
	}
	if code != 1 || stderr != "" || stdout != expectedText.String() {
		t.Fatal("omitting --format did not select text")
	}
}

func testExecutableLocalPaths(t *testing.T, binary string) {
	text := "[0] START\n[1] READY\n"
	rules, log := executableFiles(t, executableRules, text)
	// Fixture paths contain spaces and are private local files. Decimal counts
	// allow leading zeroes and accept the exact raw-byte/physical-line bounds.
	code, stdout, stderr := executableRun(t, binary, "--rules="+rules, "--format=json", "--max-input-bytes=0"+strconv.Itoa(len(text)), "--max-lines=02", "--max-line-bytes=09", log)
	if code != 0 || stderr != "" || !json.Valid([]byte(stdout)) {
		t.Fatalf("local paths/exact decimal limits: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = executableRun(t, binary, "--rules", rules, "--format", "json", "--max-input-bytes=9223372036854775807", "--max-lines=9223372036854775807", "--max-line-bytes=9223372036854775807", log)
	if code != 0 || stderr != "" || !json.Valid([]byte(stdout)) {
		t.Fatalf("maximum positive int64 limits: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	dir := t.TempDir()
	dashLog := filepath.Join(dir, "--capture.log")
	if err := os.WriteFile(dashLog, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--rules", rules, "--", "--capture.log")
	cmd.Dir = dir
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	if err := cmd.Run(); err != nil || diagnostic.Len() != 0 || !strings.HasPrefix(out.String(), "bootlog-check: PASS\n") {
		t.Fatalf("dash-prefixed path after --: err=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
	}
	link := filepath.Join(dir, "linked capture")
	if err := os.Symlink(log, link); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = executableRun(t, binary, "--rules", rules, link)
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "bootlog-check: PASS\n") {
		t.Fatalf("symlink to regular log: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func executableFiles(t *testing.T, rules, log string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	rulesPath, logPath := filepath.Join(dir, "private rules.json"), filepath.Join(dir, "private boot capture.log")
	for path, content := range map[string]string{rulesPath: rules, logPath: log} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return rulesPath, logPath
}

func executableRun(t *testing.T, binary string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("executable did not finish: %v", ctx.Err())
	}
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("launch executable: %v", err)
	}
	return exit.ExitCode(), stdout.String(), stderr.String()
}

func assertExecutableError(t *testing.T, binary string, args ...string) {
	t.Helper()
	code, stdout, stderr := executableRun(t, binary, args...)
	if code != 2 || stdout != "" || stderr == "" {
		t.Fatalf("args=%q: code=%d stdout=%q stderr=%q; want exit 2, no report, and diagnostic", args, code, stdout, stderr)
	}
}

func assertExecutableReport(t *testing.T, format, output string, want bootlog.Report) {
	t.Helper()
	if !strings.HasSuffix(output, "\n") {
		t.Fatal("report is missing its final newline")
	}
	switch format {
	case "json":
		var got bootlog.Report
		decoder := json.NewDecoder(strings.NewReader(output))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&got); err != nil {
			t.Fatalf("decode full JSON report: %v", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			t.Fatalf("trailing JSON output: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("JSON report differs:\n got %#v\nwant %#v\n%s", got, want, output)
		}
	case "text":
		assertExecutableDetails(t, output, want)
	case "junit":
		var suite struct {
			XMLName  xml.Name   `xml:"testsuite"`
			Name     string     `xml:"name,attr"`
			Tests    int        `xml:"tests,attr"`
			Failures int        `xml:"failures,attr"`
			Skipped  int        `xml:"skipped,attr"`
			Errors   int        `xml:"errors,attr"`
			Attrs    []xml.Attr `xml:",any,attr"`
			Output   string     `xml:"system-out"`
			Cases    []struct {
				Name      string     `xml:"name,attr"`
				Classname string     `xml:"classname,attr"`
				Attrs     []xml.Attr `xml:",any,attr"`
				Output    string     `xml:"system-out"`
				Failure   *struct {
					Message string `xml:"message,attr"`
				} `xml:"failure"`
				Skipped *struct {
					Message string `xml:"message,attr"`
				} `xml:"skipped"`
			} `xml:"testcase"`
		}
		decoder := xml.NewDecoder(strings.NewReader(output))
		if err := decoder.Decode(&suite); err != nil {
			t.Fatalf("decode full JUnit report: %v\n%s", err, output)
		}
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if data, ok := token.(xml.CharData); !ok || strings.TrimSpace(string(data)) != "" {
				t.Fatal("trailing XML content")
			}
		}
		if suite.Name != "bootlog-check" || suite.Tests != want.Summary.Total || suite.Failures != want.Summary.Failed || suite.Skipped != want.Summary.NotEvaluated || suite.Errors != 0 || len(suite.Cases) != len(want.Checks) {
			t.Fatalf("incomplete JUnit suite: %#v", suite)
		}
		assertNoExecutableDuration(t, suite.Attrs)
		var details strings.Builder
		details.WriteString(suite.Output)
		for i, check := range want.Checks {
			got := suite.Cases[i]
			if got.Name != check.ID || got.Classname != "bootlog."+string(check.Kind) || (got.Failure != nil) != (check.Status == bootlog.StatusFailed) || (got.Skipped != nil) != (check.Status == bootlog.StatusNotEvaluated) {
				t.Fatalf("JUnit check %d differs: %#v", i, got)
			}
			if got.Failure != nil && got.Failure.Message != string(check.Reason) || got.Skipped != nil && got.Skipped.Message != string(check.Reason) {
				t.Fatalf("JUnit check %s reason differs", check.ID)
			}
			assertNoExecutableDuration(t, got.Attrs)
			details.WriteString(got.Output)
		}
		assertExecutableDetails(t, details.String(), want)
	default:
		t.Fatalf("unknown test format %q", format)
	}
}

func assertExecutableDetails(t *testing.T, output string, want bootlog.Report) {
	t.Helper()
	headline := "bootlog-check: PASS\n"
	if want.Summary.Failed > 0 {
		headline = "bootlog-check: FAIL\n"
	}
	for _, text := range []string{
		headline,
		fmt.Sprintf("summary: total=%d passed=%d failed=%d not_evaluated=%d\n", want.Summary.Total, want.Summary.Passed, want.Summary.Failed, want.Summary.NotEvaluated),
		"schema_version: 1\n", "timing_basis: boot_start", bootlog.CaptureScope,
		"origin: " + executableEvidenceText(want.Origin),
	} {
		if !strings.Contains(output, text) {
			t.Errorf("report omits %q", text)
		}
	}
	for _, check := range want.Checks {
		text := fmt.Sprintf("[%s] %s (%s): reason=%s within_ms=%s elapsed_ns=%s match_count=%d\n  evidence: %s\n", check.Status, check.ID, check.Kind, check.Reason, executableNumber(check.WithinMS), executableNumber(check.ElapsedNS), check.MatchCount, executableEvidenceText(check.Evidence))
		if !strings.Contains(output, text) {
			t.Errorf("report omits expected check %q", text)
		}
	}
	if strings.ContainsRune(output, '\x00') {
		t.Error("unsafe NUL emitted directly")
	}
}

func assertNoExecutableDuration(t *testing.T, attrs []xml.Attr) {
	t.Helper()
	for _, attr := range attrs {
		if attr.Name.Local == "time" || attr.Name.Local == "timestamp" {
			t.Errorf("JUnit includes host-duration attribute %q", attr.Name.Local)
		}
	}
}

func executableNumber(value *int64) string {
	if value == nil {
		return "none"
	}
	return strconv.FormatInt(*value, 10)
}

func executableEvidenceText(e *bootlog.Evidence) string {
	if e == nil {
		return "none"
	}
	return fmt.Sprintf("line=%d timestamp_ns=%s message=%s", e.Line, executableNumber(e.TimestampNS), strconv.Quote(e.Message))
}

func executableInt(value int64) *int64 { return &value }

func executableEvidence(line int64, timestamp *int64, message string) *bootlog.Evidence {
	return &bootlog.Evidence{Line: line, TimestampNS: timestamp, Message: message}
}

func executableCheck(id string, kind bootlog.CheckKind, status bootlog.Status, reason bootlog.Reason, within, elapsed *int64, count int64, evidence *bootlog.Evidence) bootlog.CheckResult {
	return bootlog.CheckResult{ID: id, Kind: kind, Status: status, Reason: reason, WithinMS: within, ElapsedNS: elapsed, MatchCount: count, Evidence: evidence}
}
