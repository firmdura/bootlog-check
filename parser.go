package bootlog

import (
	"context"
	"fmt"
	"io"
	"unicode/utf8"
)

// ParseLog reads a saved UTF-8 log through EOF, retaining exact messages and
// physical line numbers. Zero options select defaults. Memory grows with the
// accepted messages and records, bounded by opts; this is not a streaming API.
// Any failure returns a zero Log. The caller owns r, including closing it.
// Cancellation is checked between reads but cannot interrupt a blocked Read.
func ParseLog(ctx context.Context, r io.Reader, opts ParseOptions) (Log, error) {
	opts, err := opts.Normalize()
	if err != nil {
		return Log{}, err
	}
	if ctx == nil {
		return Log{}, &InvalidInputError{Field: "context", Detail: "must not be nil"}
	}
	if r == nil {
		return Log{}, &InvalidInputError{Field: "reader", Detail: "must not be nil"}
	}
	var log Log
	var line []byte
	var inputBytes, lineNumber int64
	var previous timedLine
	atLineStart := true
	emptyReads := 0
	var buffer [4096]byte
	for {
		if err := ctx.Err(); err != nil {
			return Log{}, &CancellationError{Err: err}
		}
		// Probe one byte beyond the input allowance to distinguish exact EOF
		// from excess input. Never add one to an arbitrary int64 option.
		remaining := opts.MaxInputBytes - inputBytes
		readSize := len(buffer)
		if remaining < int64(readSize) {
			readSize = int(remaining) + 1
		}
		n, readErr := r.Read(buffer[:readSize])
		if err := ctx.Err(); err != nil {
			return Log{}, &CancellationError{Err: err}
		}
		if n < 0 || n > readSize {
			return Log{}, &InvalidInputError{Field: "reader", Detail: "returned an invalid byte count", Err: readErr}
		}
		if readErr != nil && readErr != io.EOF {
			return Log{}, &InvalidInputError{Field: "reader", Detail: "read failed", Err: readErr}
		}
		if int64(n) > remaining {
			return Log{}, &LimitError{Resource: "max_input_bytes", Limit: opts.MaxInputBytes}
		}
		inputBytes += int64(n)
		if n == 0 && readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				return Log{}, &InvalidInputError{Field: "reader", Detail: "read made no progress", Err: io.ErrNoProgress}
			}
		} else {
			emptyReads = 0
		}
		for _, b := range buffer[:n] {
			if atLineStart {
				if lineNumber == opts.MaxLines {
					return Log{}, &LimitError{Resource: "max_lines", Limit: opts.MaxLines}
				}
				lineNumber++
				atLineStart = false
			}
			if b == '\n' {
				content := line
				if len(content) > 0 && content[len(content)-1] == '\r' {
					content = content[:len(content)-1]
				}
				if err := appendLogLine(&log, content, lineNumber, &previous); err != nil {
					return Log{}, err
				}
				line = line[:0]
				atLineStart = true
				continue
			}
			// One extra trailing CR may belong to CRLF. Keep it pending until
			// the next byte (or EOF) establishes whether it is message data.
			if int64(len(line)) >= opts.MaxLineBytes && (int64(len(line)) > opts.MaxLineBytes || b != '\r') {
				return Log{}, lineTooLong(lineNumber, opts.MaxLineBytes)
			}
			line = append(line, b)
		}
		if readErr == io.EOF {
			if int64(len(line)) > opts.MaxLineBytes {
				return Log{}, lineTooLong(lineNumber, opts.MaxLineBytes)
			}
			if !atLineStart {
				if err := appendLogLine(&log, line, lineNumber, &previous); err != nil {
					return Log{}, err
				}
			}
			return log, nil
		}
	}
}

func lineTooLong(lineNumber, maxBytes int64) error {
	return fmt.Errorf("line %d: %w", lineNumber, &LimitError{Resource: "max_line_bytes", Limit: maxBytes})
}

type timedLine struct {
	number, ns int64
}

func appendLogLine(log *Log, line []byte, lineNumber int64, previous *timedLine) error {
	if len(line) == 0 {
		return nil
	}
	if !utf8.Valid(line) {
		offset := 0
		for offset < len(line) {
			r, size := utf8.DecodeRune(line[offset:])
			if r == utf8.RuneError && size == 1 {
				break
			}
			offset += size
		}
		return &InvalidInputError{
			Line:   lineNumber,
			Field:  "message",
			Detail: fmt.Sprintf("must be valid UTF-8; invalid sequence starts at byte %d", offset+1),
		}
	}
	timestamp, message, err := parseLogTimestamp(line, lineNumber)
	if err != nil {
		return err
	}
	if timestamp != nil {
		if previous.number != 0 && *timestamp < previous.ns {
			return &InvalidInputError{
				Line:   lineNumber,
				Field:  "timestamp_ns",
				Detail: fmt.Sprintf("must be nondecreasing; previous timestamp at line %d", previous.number),
			}
		}
		*previous = timedLine{number: lineNumber, ns: *timestamp}
	}
	log.Records = append(log.Records, Record{Line: lineNumber, TimestampNS: timestamp, Message: string(message)})
	return nil
}
