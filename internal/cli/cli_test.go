package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

const testRules = `{"schema_version":1,"boot_start":{"pattern":"^BOOT$"},"milestones":[{"id":"ready","pattern":"^READY$","within_ms":1000}]}`
const testLog = "[1] BOOT\n[2] READY\n"

var errInjected = errors.New("injected failure")

type inputFile struct {
	io.Reader
	mode     os.FileMode
	statErr  error
	closeErr error
	closed   int
}

func (f *inputFile) Stat() (os.FileInfo, error) { return fileInfo{f.mode}, f.statErr }
func (f *inputFile) Close() error               { f.closed++; return f.closeErr }

type fileInfo struct{ mode os.FileMode }

func (fileInfo) Name() string        { return "input" }
func (fileInfo) Size() int64         { return 0 }
func (f fileInfo) Mode() os.FileMode { return f.mode }
func (fileInfo) ModTime() time.Time  { return time.Time{} }
func (fileInfo) IsDir() bool         { return false }
func (fileInfo) Sys() any            { return nil }

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func inputs() (*inputFile, *inputFile) {
	return &inputFile{Reader: strings.NewReader(testRules)}, &inputFile{Reader: strings.NewReader(testLog)}
}

func streamInputs(out, stderr io.Writer, rules, log *inputFile) Streams {
	return Streams{Stdout: out, Stderr: stderr, Open: func(path string) (File, error) {
		switch path {
		case "rules":
			return rules, nil
		case "log":
			return log, nil
		default:
			return nil, errInjected
		}
	}}
}

func TestRunClosesInputsBeforeOutput(t *testing.T) {
	rules, log := inputs()
	var diagnostic, output bytes.Buffer
	out := writerFunc(func(p []byte) (int, error) {
		if rules.closed != 1 || log.closed != 1 {
			t.Fatalf("rendered before closing files: rules=%d log=%d", rules.closed, log.closed)
		}
		return output.Write(p)
	})
	code := Run(context.Background(), []string{"--rules", "rules", "log"}, streamInputs(out, &diagnostic, rules, log))
	if code != 0 || diagnostic.Len() != 0 || !strings.Contains(output.String(), "bootlog-check: PASS") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, output.String(), diagnostic.String())
	}
}

func TestRunDuplicateBootStartNamesBothLines(t *testing.T) {
	rules, log := inputs()
	log.Reader = strings.NewReader("preamble\n\n[1] BOOT\n[2] READY\n\n[3] BOOT\n")
	var out, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--rules", "rules", "log"}, streamInputs(&out, &stderr, rules, log))
	msg := stderr.String()
	if code != 2 || out.Len() != 0 || !strings.Contains(msg, "line 6") || !strings.Contains(msg, "first match at line 3") {
		t.Fatalf("code=%d stdout=%q stderr=%q; want both boot-start lines on stderr", code, out.String(), msg)
	}
}

func TestRunInputFailuresNeverRender(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*inputFile, *inputFile)
		closed [2]int
	}{
		{"rules reader", func(r, l *inputFile) { r.Reader = readerFunc(func([]byte) (int, error) { return 0, errInjected }) }, [2]int{1, 0}},
		{"rules data and error", func(r, l *inputFile) {
			r.Reader = readerFunc(func(p []byte) (int, error) { return copy(p, testRules), errInjected })
		}, [2]int{1, 0}},
		{"rules close", func(r, l *inputFile) { r.closeErr = errInjected }, [2]int{1, 0}},
		{"rules stat", func(r, l *inputFile) { r.statErr = errInjected }, [2]int{1, 0}},
		{"rules directory", func(r, l *inputFile) { r.mode = os.ModeDir }, [2]int{1, 0}},
		{"log reader", func(r, l *inputFile) { l.Reader = readerFunc(func([]byte) (int, error) { return 0, errInjected }) }, [2]int{1, 1}},
		{"log after ready reader", func(r, l *inputFile) {
			l.Reader = io.MultiReader(strings.NewReader(testLog), readerFunc(func([]byte) (int, error) { return 0, errInjected }))
		}, [2]int{1, 1}},
		{"log close", func(r, l *inputFile) { l.closeErr = errInjected }, [2]int{1, 1}},
		{"log stat", func(r, l *inputFile) { l.statErr = errInjected }, [2]int{1, 1}},
		{"log device", func(r, l *inputFile) { l.mode = os.ModeDevice }, [2]int{1, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules, log := inputs()
			tc.change(rules, log)
			var out, stderr bytes.Buffer
			code := Run(context.Background(), []string{"--rules", "rules", "log"}, streamInputs(&out, &stderr, rules, log))
			if code != 2 || out.Len() != 0 || stderr.Len() == 0 || rules.closed != tc.closed[0] || log.closed != tc.closed[1] {
				t.Fatalf("code=%d stdout=%q stderr=%q closed=(%d,%d)", code, out.String(), stderr.String(), rules.closed, log.closed)
			}
		})
	}
}

func TestRunOutputFailure(t *testing.T) {
	for _, args := range [][]string{{"--rules", "rules", "log"}, {"--help"}, {"-h"}, {"--version"}} {
		for _, tc := range []struct {
			name string
			w    io.Writer
			want string
		}{
			{"failed", writerFunc(func([]byte) (int, error) { return 0, errInjected }), "injected failure"},
			{"partial", writerFunc(func(p []byte) (int, error) { return len(p) / 2, errInjected }), "injected failure"},
			{"short", writerFunc(func(p []byte) (int, error) { return len(p) - 1, nil }), "short write"},
		} {
			t.Run(strings.Join(args, " ")+"/"+tc.name, func(t *testing.T) {
				rules, log := inputs()
				var stderr bytes.Buffer
				if code := Run(context.Background(), args, streamInputs(tc.w, &stderr, rules, log)); code != 2 || !strings.Contains(stderr.String(), tc.want) {
					t.Fatalf("code=%d stderr=%q", code, stderr.String())
				}
			})
		}
	}
}

func TestRunShortHelp(t *testing.T) {
	for _, args := range [][]string{
		{"-h"},
		{"-h", "--rules", "missing-rules", "missing-log"},
		{"--version", "-h"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, stderr bytes.Buffer
			streams := Streams{Stdout: &out, Stderr: &stderr, Open: func(path string) (File, error) {
				t.Fatalf("help opened input %q", path)
				return nil, errInjected
			}}
			code := Run(context.Background(), args, streams)
			if code != 0 || !strings.HasPrefix(out.String(), "Usage: bootlog-check ") || stderr.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q; want help on stdout", code, out.String(), stderr.String())
			}
		})
	}
}

func TestRunCancellation(t *testing.T) {
	for _, phase := range []string{"before input", "rules read", "rules close", "log read", "log close", "output"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rules, log := inputs()
			var out, stderr bytes.Buffer
			streams := streamInputs(&out, &stderr, rules, log)
			switch phase {
			case "before input":
				cancel()
			case "rules read":
				rules.Reader = readerFunc(func(p []byte) (int, error) { cancel(); return copy(p, testRules), io.EOF })
			case "log read":
				log.Reader = readerFunc(func(p []byte) (int, error) { cancel(); return copy(p, testLog), io.EOF })
			case "rules close", "log close":
				streams.Open = func(path string) (File, error) {
					f := rules
					if path == "log" {
						f = log
					}
					if (path == "rules" && phase == "rules close") || (path == "log" && phase == "log close") {
						return cancelOnClose{f, cancel}, nil
					}
					return f, nil
				}
			case "output":
				streams.Stdout = writerFunc(func(p []byte) (int, error) { cancel(); return out.Write(p) })
			}
			code := Run(ctx, []string{"--rules", "rules", "log"}, streams)
			if code != 2 || !strings.Contains(stderr.String(), "context canceled") || (phase != "output" && out.Len() != 0) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
			}
			if phase != "before input" && rules.closed != 1 {
				t.Errorf("rules closed %d times", rules.closed)
			}
		})
	}
}

type cancelOnClose struct {
	*inputFile
	cancel context.CancelFunc
}

func (f cancelOnClose) Close() error { f.cancel(); return f.inputFile.Close() }

func TestRunCheckRulesCanceledOnClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rules, _ := inputs()
	var out, stderr bytes.Buffer
	streams := Streams{Stdout: &out, Stderr: &stderr, Open: func(path string) (File, error) {
		if path != "rules" {
			t.Fatalf("unexpected input %q", path)
		}
		return cancelOnClose{rules, cancel}, nil
	}}
	code := Run(ctx, []string{"--check-rules", "rules"}, streams)
	if code != 2 || rules.closed != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("code=%d closed=%d stdout=%q stderr=%q", code, rules.closed, out.String(), stderr.String())
	}
}

func TestRunInvalidDependencies(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		open func(string) (File, error)
	}{
		{"nil context", nil, nil},
		{"open failure", context.Background(), func(string) (File, error) { return nil, errInjected }},
		{"nil opened file", context.Background(), func(string) (File, error) { return nil, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			code := Run(tc.ctx, []string{"--rules", "rules", "log"}, Streams{Stdout: &out, Stderr: &stderr, Open: tc.open})
			if code != 2 || out.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
			}
		})
	}
	if code := Run(context.Background(), []string{"--help"}, Streams{}); code != 2 {
		t.Fatalf("nil stdout: code=%d", code)
	}
	if code := Run(context.Background(), []string{"--unknown"}, Streams{Stdout: io.Discard}); code != 2 {
		t.Fatalf("nil stderr: code=%d", code)
	}
}
