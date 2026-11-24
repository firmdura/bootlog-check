package bootlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// ParseRules reads one strict JSON rule document, bounded by MaxRulesBytes.
// It validates all configuration, including patterns, before returning a Config.
// The caller owns r; ParseRules never closes it. Use CompileRules to retain the
// compiled expressions for repeated or concurrent evaluations.
func ParseRules(r io.Reader) (Config, error) {
	if r == nil {
		return Config{}, rulesError("", "reader must not be nil", nil)
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxRulesBytes+1))
	if int64(len(data)) > MaxRulesBytes {
		return Config{}, &LimitError{Resource: "rules bytes", Limit: MaxRulesBytes, Err: err}
	}
	if err != nil {
		return Config{}, rulesError("", "read failed", err)
	}
	if !utf8.Valid(data) || !validRulesUnicode(data) {
		return Config{}, rulesError("", "must contain valid Unicode strings", nil)
	}
	fields, err := ruleObject(data, "", "schema_version", "boot_start", "milestones", "forbidden")
	if err != nil {
		return Config{}, locateRulesSyntaxError(data, err)
	}
	var cfg Config
	if err := ruleValue(fields, "schema_version", "schema_version", true, &cfg.SchemaVersion); err != nil {
		return Config{}, err
	}
	start, err := ruleObject(fields["boot_start"], "boot_start", "pattern")
	if err != nil {
		return Config{}, err
	}
	if err := ruleValue(start, "pattern", "boot_start.pattern", true, &cfg.BootStart.Pattern); err != nil {
		return Config{}, err
	}
	var milestones, forbidden []json.RawMessage
	if err := ruleValue(fields, "milestones", "milestones", false, &milestones); err != nil {
		return Config{}, err
	}
	if err := ruleValue(fields, "forbidden", "forbidden", false, &forbidden); err != nil {
		return Config{}, err
	}
	if len(milestones) > MaxUserRules || len(forbidden) > MaxUserRules || len(milestones)+len(forbidden) > MaxUserRules {
		return Config{}, rulesError("rules", "must contain at most 128 user rules", nil)
	}
	for i, raw := range milestones {
		field := fmt.Sprintf("milestones[%d]", i)
		values, err := ruleObject(raw, field, "id", "pattern", "within_ms")
		if err != nil {
			return Config{}, err
		}
		var rule MilestoneConfig
		if err := ruleValue(values, "id", field+".id", true, &rule.ID); err != nil {
			return Config{}, err
		}
		if err := ruleValue(values, "pattern", field+".pattern", true, &rule.Pattern); err != nil {
			return Config{}, err
		}
		if _, exists := values["within_ms"]; exists {
			var deadline int64
			if err := ruleValue(values, "within_ms", field+".within_ms", true, &deadline); err != nil {
				return Config{}, err
			}
			rule.WithinMS = &deadline
		}
		cfg.Milestones = append(cfg.Milestones, rule)
	}
	for i, raw := range forbidden {
		field := fmt.Sprintf("forbidden[%d]", i)
		values, err := ruleObject(raw, field, "id", "pattern")
		if err != nil {
			return Config{}, err
		}
		var rule ForbiddenConfig
		if err := ruleValue(values, "id", field+".id", true, &rule.ID); err != nil {
			return Config{}, err
		}
		if err := ruleValue(values, "pattern", field+".pattern", true, &rule.Pattern); err != nil {
			return Config{}, err
		}
		cfg.Forbidden = append(cfg.Forbidden, rule)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func rulesError(field, detail string, err error) error {
	return &InvalidRulesError{Field: field, Detail: detail, Err: err}
}

func locateRulesSyntaxError(data []byte, err error) error {
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	// Token and Decode errors from individual fields need not carry a
	// document-relative offset. Rescan only a rejected, size-bounded document.
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	offset := len(data) // An unfinished document points just past the last byte.
	switch scanErr := dec.Decode(&raw); {
	case errors.As(scanErr, &syntax):
		offset = int(syntax.Offset - 1)
	case scanErr == nil:
		// The first value was complete; point to the unexpected trailing data.
		offset -= len(bytes.TrimLeft(data[dec.InputOffset():], " \t\r\n"))
	}
	before := data[:offset]
	line := 1 + bytes.Count(before, []byte{'\n'})
	column := offset - bytes.LastIndexByte(before, '\n')
	return fmt.Errorf("%w (line %d, byte column %d)", err, line, column)
}

// Read keys individually: encoding/json's struct and map decoders accept duplicate
// keys, and struct decoding also accepts case-insensitive field names.
func ruleObject(data []byte, field string, allowed ...string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil {
		return nil, rulesError(field, "expected object", err)
	}
	if token != json.Delim('{') {
		return nil, rulesError(field, "expected non-null object", nil)
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, rulesError(field, "invalid object key", err)
		}
		key, ok := token.(string)
		if !ok {
			return nil, rulesError(field, "expected string key", nil)
		}
		if _, exists := fields[key]; exists {
			return nil, rulesError(field, "duplicate key "+key, nil)
		}
		known := false
		for _, name := range allowed {
			known = known || key == name
		}
		if !known {
			return nil, rulesError(field, "unknown key "+key, nil)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, rulesError(field, "invalid value for "+key, err)
		}
		fields[key] = raw
	}
	if _, err := dec.Token(); err != nil {
		return nil, rulesError(field, "invalid object", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, rulesError(field, "expected exactly one JSON value", err)
	}
	return fields, nil
}

func ruleValue(fields map[string]json.RawMessage, key, field string, required bool, dst any) error {
	raw, exists := fields[key]
	if !exists {
		if required {
			return rulesError(field, "required field is missing", nil)
		}
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return rulesError(field, "must not be null", nil)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return rulesError(field, "invalid value or type", err)
	}
	return nil
}

// encoding/json replaces unpaired UTF-16 surrogate escapes with U+FFFD. Reject
// those escapes instead of silently altering a pattern or ID. Other JSON syntax
// is checked by the decoder. Literal U+FFFD remains valid Unicode.
func validRulesUnicode(data []byte) bool {
	inString := false
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '"':
			inString = !inString
		case '\\':
			if !inString {
				continue
			}
			i++
			if i >= len(data) {
				return false
			}
			if data[i] != 'u' {
				continue
			}
			u, ok := ruleHex4(data[i+1:])
			if !ok {
				return false
			}
			i += 4
			if u >= 0xdc00 && u <= 0xdfff {
				return false
			}
			if u >= 0xd800 && u <= 0xdbff {
				if len(data)-i < 7 || data[i+1] != '\\' || data[i+2] != 'u' {
					return false
				}
				low, ok := ruleHex4(data[i+3:])
				if !ok || low < 0xdc00 || low > 0xdfff {
					return false
				}
				i += 6
			}
		}
	}
	return true
}

func ruleHex4(data []byte) (uint16, bool) {
	if len(data) < 4 {
		return 0, false
	}
	var n uint16
	for _, b := range data[:4] {
		n <<= 4
		switch {
		case b >= '0' && b <= '9':
			n += uint16(b - '0')
		case b >= 'a' && b <= 'f':
			n += uint16(b-'a') + 10
		case b >= 'A' && b <= 'F':
			n += uint16(b-'A') + 10
		default:
			return 0, false
		}
	}
	return n, true
}
