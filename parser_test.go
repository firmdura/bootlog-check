package bootlog_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	bootlog "github.com/firmdura/bootlog-check"
)

func parserNS(value int64) *int64 { return &value }

func TestParseLogMessages(t *testing.T) {
	tests := []struct {
		name, input string
		want        []bootlog.Record
	}{
		{"empty", "", nil},
		{"empty lines", "\n\r\n\n", nil},
		{"line numbers", "\n\r\n \n\t\r\nlast", []bootlog.Record{{Line: 3, Message: " "}, {Line: 4, Message: "\t"}, {Line: 5, Message: "last"}}},
		{"LF", "first\n[0] second\n", []bootlog.Record{{Line: 1, Message: "first"}, {Line: 2, TimestampNS: parserNS(0), Message: "second"}}},
		{"CRLF", "first\r\n[0] second\r\n", []bootlog.Record{{Line: 1, Message: "first"}, {Line: 2, TimestampNS: parserNS(0), Message: "second"}}},
		{"no final newline", "first\n[0] second", []bootlog.Record{{Line: 1, Message: "first"}, {Line: 2, TimestampNS: parserNS(0), Message: "second"}}},
		{"prefix only", "[0]\n[0] \n[0]\t", []bootlog.Record{{Line: 1, TimestampNS: parserNS(0)}, {Line: 2, TimestampNS: parserNS(0)}, {Line: 3, TimestampNS: parserNS(0)}}},
		{"one separator", "[ \t0]\t \tx\n[0]  \n[0]x", []bootlog.Record{{Line: 1, TimestampNS: parserNS(0), Message: " \tx"}, {Line: 2, TimestampNS: parserNS(0), Message: " "}, {Line: 3, TimestampNS: parserNS(0), Message: "x"}}},
		{"control bytes", "\x1b[31mpreamble\x00\r\r\n[0]\x1b[0m\x07\t\x00\r", []bootlog.Record{{Line: 1, Message: "\x1b[31mpreamble\x00\r"}, {Line: 2, TimestampNS: parserNS(0), Message: "\x1b[0m\x07\t\x00\r"}}},
		{"unicode", "Boot ü世界\n[1.000000001] 🙂", []bootlog.Record{{Line: 1, Message: "Boot ü世界"}, {Line: 2, TimestampNS: parserNS(1_000_000_001), Message: "🙂"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := bootlog.ParseLog(context.Background(), strings.NewReader(test.input), bootlog.ParseOptions{})
			if err != nil || !reflect.DeepEqual(got.Records, test.want) {
				t.Fatalf("ParseLog() = %#v, %v; want %#v", got, err, test.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("invalid successful model: %v", err)
			}
		})
	}
}

func TestParseLogOrdinaryBracketedMessages(t *testing.T) {
	for _, input := range []string{"[", "[ \t", "[ \t]", "[ OK ] ready", " [1] x", "\t[1] x", "[\v1] x", "[NaN]", "[Inf]", "[١]", "[１]"} {
		t.Run(input, func(t *testing.T) {
			log, err := bootlog.ParseLog(context.Background(), strings.NewReader(input), bootlog.ParseOptions{})
			if err != nil || len(log.Records) != 1 || log.Records[0].TimestampNS != nil || log.Records[0].Message != input {
				t.Fatalf("ordinary bracketed message = %#v, %v", log, err)
			}
		})
	}
}

func TestParseLogExactTimestamps(t *testing.T) {
	tests := []struct {
		prefix string
		want   int64
	}{
		{"[0]", 0}, {"[1]", 1_000_000_000}, {"[42]", 42_000_000_000},
		{"[0.1]", 100_000_000}, {"[0.12]", 120_000_000}, {"[0.123]", 123_000_000},
		{"[0.1234]", 123_400_000}, {"[0.12345]", 123_450_000}, {"[0.123456]", 123_456_000},
		{"[0.1234567]", 123_456_700}, {"[0.12345678]", 123_456_780}, {"[0.123456789]", 123_456_789},
		{"[0.000000001]", 1}, {"[0.000000000]", 0}, {"[01.250000]", 1_250_000_000},
		{"[\t   1.000000001]", 1_000_000_001}, {"[0000000000000000000000000000001.000000001]", 1_000_000_001},
		{"[9223372036]", 9_223_372_036_000_000_000}, {"[9223372036.854775807]", math.MaxInt64},
	}
	for _, test := range tests {
		t.Run(test.prefix, func(t *testing.T) {
			log, err := bootlog.ParseLog(context.Background(), strings.NewReader(test.prefix+" msg"), bootlog.ParseOptions{})
			if err != nil || len(log.Records) != 1 {
				t.Fatalf("ParseLog() = %#v, %v", log, err)
			}
			record := log.Records[0]
			if record.TimestampNS == nil || *record.TimestampNS != test.want || record.Message != "msg" {
				t.Fatalf("record = %#v; want timestamp %d, message msg", record, test.want)
			}
		})
	}
}

func TestParseLogInvalidTimestamps(t *testing.T) {
	for _, input := range []string{
		"[+0]", "[-0]", "[-1]", "[.5]", "[.]", "[  +1]", "[\t-1]", "[1.]", "[1. ]", "[1.2.3]",
		"[1e0]", "[1E2]", "[0x1]", "[1 ]", "[1\t]", "[1", "[1.2", "[1\x00]", "[1,5]", "[1٢]",
		"[0.0000000000]", "[1.1234567890]", "[9223372036.854775808]", "[9223372037]", "[9999999999999999999999999999999]",
	} {
		t.Run(input, func(t *testing.T) {
			log, err := bootlog.ParseLog(context.Background(), strings.NewReader("\n"+input), bootlog.ParseOptions{})
			assertParserFailure(t, log, err, bootlog.ErrInvalidInput)
			var inputErr *bootlog.InvalidInputError
			if !errors.As(err, &inputErr) || inputErr.Line != 2 || inputErr.Field != "timestamp_ns" {
				t.Fatalf("missing timestamp error location: %v", err)
			}
		})
	}
}

func TestParseLogTimestampOrder(t *testing.T) {
	for _, tc := range []struct {
		input          string
		line, previous int64
	}{
		{"[2] bootloader\n\nuntimed\n[1] Linux version", 4, 1},
		{"[0] Linux version\n[2] APP_READY\n[2] same time\r\nuntimed\r\n\n[1] reboot\n", 6, 3},
		{"[1.000000001]\n[1.000000000]", 2, 1},
	} {
		log, err := bootlog.ParseLog(context.Background(), strings.NewReader(tc.input), bootlog.ParseOptions{})
		assertParserFailure(t, log, err, bootlog.ErrInvalidInput)
		var invalid *bootlog.InvalidInputError
		if !errors.As(err, &invalid) || invalid.Line != tc.line || invalid.Field != "timestamp_ns" ||
			!strings.Contains(invalid.Detail, fmt.Sprintf("previous timestamp at line %d", tc.previous)) {
			t.Errorf("want timestamp error at line %d referring to line %d, got %v", tc.line, tc.previous, err)
		}
	}
	log, err := bootlog.ParseLog(context.Background(), strings.NewReader("[1] preamble\nuntimed\n[1] Linux version\n[1.000000001] next"), bootlog.ParseOptions{})
	if err != nil || len(log.Records) != 4 || log.Records[1].TimestampNS != nil || *log.Records[0].TimestampNS != 1_000_000_000 || *log.Records[3].TimestampNS != 1_000_000_001 {
		t.Fatalf("equal timestamps and untimed lines: %#v, %v", log, err)
	}
}

func TestParseLogLimits(t *testing.T) {
	tests := []struct {
		name, input, resource string
		opts                  bootlog.ParseOptions
		fail                  bool
	}{
		{"bytes below", "a\n", "max_input_bytes", bootlog.ParseOptions{MaxInputBytes: 3}, false},
		{"bytes exact", "a\r\n", "max_input_bytes", bootlog.ParseOptions{MaxInputBytes: 3}, false},
		{"bytes excess", "a\r\n\n", "max_input_bytes", bootlog.ParseOptions{MaxInputBytes: 3}, true},
		{"bytes final below", "ab", "max_input_bytes", bootlog.ParseOptions{MaxInputBytes: 3}, false},
		{"bytes final exact", "abc", "max_input_bytes", bootlog.ParseOptions{MaxInputBytes: 3}, false},
		{"bytes final excess", "abcd", "max_input_bytes", bootlog.ParseOptions{MaxInputBytes: 3}, true},
		{"lines below", "", "max_lines", bootlog.ParseOptions{MaxLines: 1}, false},
		{"lines exact", "\n", "max_lines", bootlog.ParseOptions{MaxLines: 1}, false},
		{"lines excess", "\n\n", "max_lines", bootlog.ParseOptions{MaxLines: 1}, true},
		{"no phantom line", "a\n", "max_lines", bootlog.ParseOptions{MaxLines: 1}, false},
		{"unterminated line", "a", "max_lines", bootlog.ParseOptions{MaxLines: 1}, false},
		{"blank after final", "a\n\n", "max_lines", bootlog.ParseOptions{MaxLines: 1}, true},
		{"unterminated excess", "a\nb", "max_lines", bootlog.ParseOptions{MaxLines: 1}, true},
		{"line below", "ab\n", "max_line_bytes", bootlog.ParseOptions{MaxLineBytes: 3}, false},
		{"line exact", "abc\n", "max_line_bytes", bootlog.ParseOptions{MaxLineBytes: 3}, false},
		{"line excess", "abcd\n", "max_line_bytes", bootlog.ParseOptions{MaxLineBytes: 3}, true},
		{"CRLF exact", "abc\r\n", "max_line_bytes", bootlog.ParseOptions{MaxLineBytes: 3}, false},
		{"CRLF excess", "abcd\r\n", "max_line_bytes", bootlog.ParseOptions{MaxLineBytes: 3}, true},
		{"extra CR exact", "ab\r\r\n", "max_line_bytes", bootlog.ParseOptions{MaxLineBytes: 3}, false},
		{"extra CR excess", "abc\r\r\n", "max_line_bytes", bootlog.ParseOptions{MaxLineBytes: 3}, true},
		{"lone CR exact", "ab\r", "max_line_bytes", bootlog.ParseOptions{MaxLineBytes: 3}, false},
		{"lone CR excess", "abc\r", "max_line_bytes", bootlog.ParseOptions{MaxLineBytes: 3}, true},
		{"UTF8 bytes exact", "éx", "max_line_bytes", bootlog.ParseOptions{MaxLineBytes: 3}, false},
		{"UTF8 bytes excess", "éxx", "max_line_bytes", bootlog.ParseOptions{MaxLineBytes: 3}, true},
		{"prefix counts", "[0]x", "max_line_bytes", bootlog.ParseOptions{MaxLineBytes: 3}, true},
		{"huge options no preallocation", "[0]x", "", bootlog.ParseOptions{MaxInputBytes: math.MaxInt64, MaxLines: math.MaxInt64, MaxLineBytes: math.MaxInt64}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// One-byte reads exercise pending CRLF and raw byte boundaries.
			log, err := bootlog.ParseLog(context.Background(), &parserChunkReader{input: []byte(test.input), chunk: 1}, test.opts)
			if test.fail {
				assertParserFailure(t, log, err, bootlog.ErrLimit)
				var limitErr *bootlog.LimitError
				if !errors.As(err, &limitErr) || limitErr.Resource != test.resource {
					t.Fatalf("limit = %v; want %s", err, test.resource)
				}
			} else if err != nil {
				t.Fatalf("unexpected failure: %v", err)
			}
		})
	}
}

func TestParseLogDefaultAndRaisedLineCap(t *testing.T) {
	for _, size := range []int{int(bootlog.DefaultMaxLineBytes) - 1, int(bootlog.DefaultMaxLineBytes), int(bootlog.DefaultMaxLineBytes) + 1} {
		input := strings.Repeat("x", size)
		log, err := bootlog.ParseLog(context.Background(), strings.NewReader(input), bootlog.ParseOptions{})
		if int64(size) > bootlog.DefaultMaxLineBytes {
			assertParserFailure(t, log, err, bootlog.ErrLimit)
		} else if err != nil || log.Records[0].Message != input {
			t.Fatalf("default cap at %d: %v", size, err)
		}
	}
	input := strings.Repeat("x", int(bootlog.DefaultMaxLineBytes)+1)
	log, err := bootlog.ParseLog(context.Background(), strings.NewReader(input), bootlog.ParseOptions{MaxLineBytes: int64(len(input))})
	if err != nil || log.Validate() != nil || log.Records[0].Message != input {
		t.Fatalf("raised cap: %v", err)
	}
}

func TestParseLogLineLimitReportsPhysicalLine(t *testing.T) {
	const prefix = "\n[0] BOOT\r\n\n"
	for _, tail := range []string{"raw data!\r\n", "raw data\r"} {
		log, err := bootlog.ParseLog(context.Background(), strings.NewReader(prefix+tail), bootlog.ParseOptions{MaxLineBytes: 8})
		assertParserFailure(t, log, err, bootlog.ErrLimit)
		var limit *bootlog.LimitError
		if !errors.As(err, &limit) || limit.Resource != "max_line_bytes" || limit.Limit != 8 {
			t.Fatalf("tail %q: want the per-line limit of 8 bytes, got %v", tail, err)
		}
		if !strings.Contains(err.Error(), "line 4") || strings.Contains(err.Error(), "raw data") {
			t.Errorf("tail %q: want the physical line number without its contents, got %v", tail, err)
		}
	}
}

func TestParseLogInvalidUTF8Location(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		line, pos   int64
	}{
		{"first byte", "\xfftext\xff\n", 1, 1},
		{"after timestamp and multibyte text", "header\r\n\r\n[1] é\uFFFD\xff\xff\r\n", 3, 10},
		{"incomplete sequence at EOF", "[0] OK\nend \xe2\x82", 2, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log, err := bootlog.ParseLog(context.Background(), &parserChunkReader{input: []byte(tc.input), chunk: 1}, bootlog.ParseOptions{})
			assertParserFailure(t, log, err, bootlog.ErrInvalidInput)
			var invalid *bootlog.InvalidInputError
			want := fmt.Sprintf("must be valid UTF-8; invalid sequence starts at byte %d", tc.pos)
			if !errors.As(err, &invalid) || invalid.Line != tc.line || invalid.Field != "message" || invalid.Detail != want {
				t.Errorf("want encoding error at line %d, byte %d; got %v", tc.line, tc.pos, err)
			}
		})
	}
}

func TestParseLogLateFailures(t *testing.T) {
	prefix := "[0] Linux version\n[1] APP_READY\n"
	for _, suffix := range []string{"[+2] bad", "[2.0000000000] bad", "invalid\xff", "\xc3"} {
		log, err := bootlog.ParseLog(context.Background(), strings.NewReader(prefix+suffix), bootlog.ParseOptions{})
		assertParserFailure(t, log, err, bootlog.ErrInvalidInput)
	}
	for _, opts := range []bootlog.ParseOptions{
		{MaxLines: 2}, {MaxInputBytes: int64(len(prefix))}, {MaxLineBytes: 20},
	} {
		log, err := bootlog.ParseLog(context.Background(), strings.NewReader(prefix+strings.Repeat("x", 21)), opts)
		assertParserFailure(t, log, err, bootlog.ErrLimit)
	}
}

func assertParserFailure(t *testing.T, log bootlog.Log, err error, category error) {
	t.Helper()
	if !errors.Is(err, category) || !reflect.DeepEqual(log, bootlog.Log{}) {
		t.Fatalf("ParseLog() = %#v, %v; want zero Log and %v", log, err, category)
	}
}
