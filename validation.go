package bootlog

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

// Validate checks caller-built records without applying parser size limits.
// Callers constructing a Log directly own their memory and input-size bounds.
func (l Log) Validate() error {
	var previousLine, previousTime int64
	for i, record := range l.Records {
		if record.Line <= previousLine {
			return &InvalidInputError{
				Line: record.Line, Field: fmt.Sprintf("records[%d].line", i),
				Detail: "must be positive and strictly increasing",
			}
		}
		if !utf8.ValidString(record.Message) {
			return &InvalidInputError{
				Line: record.Line, Field: fmt.Sprintf("records[%d].message", i),
				Detail: "must be valid UTF-8",
			}
		}
		if record.TimestampNS != nil {
			if *record.TimestampNS < previousTime {
				return &InvalidInputError{
					Line: record.Line, Field: fmt.Sprintf("records[%d].timestamp_ns", i),
					Detail: "must be nonnegative and nondecreasing",
				}
			}
			previousTime = *record.TimestampNS
		}
		previousLine = record.Line
	}
	return nil
}

// Validate checks caller-built configuration, including Go regexp syntax and
// empty-string matches. Nil rule slices mean empty. Strict JSON parsing must
// separately reject missing required fields, null values, and duplicate keys.
func (c Config) Validate() error {
	_, err := validateConfig(c)
	return err
}

// Return compiled expressions so CompileRules can reuse the validation pass.
// This helper does not retain the caller's configuration or construct a RuleSet.
func validateConfig(c Config) (compiledPatterns, error) {
	var patterns compiledPatterns
	invalid := func(field, detail string) (compiledPatterns, error) {
		return compiledPatterns{}, &InvalidRulesError{Field: field, Detail: detail}
	}
	if c.SchemaVersion != SchemaVersion {
		return invalid("schema_version", "must equal 1")
	}
	if len(c.Milestones) > MaxUserRules || len(c.Forbidden) > MaxUserRules || len(c.Milestones)+len(c.Forbidden) > MaxUserRules {
		return invalid("rules", "must contain at most 128 user rules")
	}
	if len(c.Milestones)+len(c.Forbidden) == 0 {
		return invalid("rules", "must contain at least one milestone or forbidden rule")
	}
	var err error
	patterns.bootStart, err = compilePattern(c.BootStart.Pattern, "boot_start.pattern")
	if err != nil {
		return compiledPatterns{}, err
	}
	idFields := make(map[string]string)
	checkID := func(id, field string) error {
		if !validID(id) || id == BootStartID {
			return &InvalidRulesError{Field: field, Detail: "must be a unique, nonreserved rule ID"}
		}
		if first, ok := idFields[id]; ok {
			return &InvalidRulesError{Field: field, Detail: "duplicates " + first}
		}
		idFields[id] = field
		return nil
	}
	for i, rule := range c.Milestones {
		field := fmt.Sprintf("milestones[%d]", i)
		if err := checkID(rule.ID, field+".id"); err != nil {
			return compiledPatterns{}, err
		}
		if rule.WithinMS != nil && (*rule.WithinMS < 0 || *rule.WithinMS > MaxWithinMS) {
			return invalid(field+".within_ms", "must be nonnegative and fit in int64 nanoseconds")
		}
		pattern, err := compilePattern(rule.Pattern, field+".pattern")
		if err != nil {
			return compiledPatterns{}, err
		}
		patterns.milestones = append(patterns.milestones, pattern)
	}
	for i, rule := range c.Forbidden {
		field := fmt.Sprintf("forbidden[%d]", i)
		if err := checkID(rule.ID, field+".id"); err != nil {
			return compiledPatterns{}, err
		}
		pattern, err := compilePattern(rule.Pattern, field+".pattern")
		if err != nil {
			return compiledPatterns{}, err
		}
		patterns.forbidden = append(patterns.forbidden, pattern)
	}
	return patterns, nil
}

func compilePattern(pattern, field string) (*regexp.Regexp, error) {
	if len(pattern) == 0 || len(pattern) > MaxPatternBytes || !utf8.ValidString(pattern) {
		return nil, &InvalidRulesError{Field: field, Detail: "must be nonempty valid UTF-8 of at most 4096 bytes"}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, &InvalidRulesError{Field: field, Detail: "invalid Go regexp", Err: err}
	}
	if re.MatchString("") {
		return nil, &InvalidRulesError{Field: field, Detail: "must not match the empty string"}
	}
	return re, nil
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 64 || !asciiLetter(id[0]) {
		return false
	}
	for i := 1; i < len(id); i++ {
		c := id[i]
		if !asciiLetter(c) && !(c >= '0' && c <= '9') && c != '_' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

func asciiLetter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }
