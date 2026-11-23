package bootlog_test

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	bootlog "github.com/firmdura/bootlog-check"
)

func validationInt64(value int64) *int64 { return &value }

func validationConfig() bootlog.Config {
	return bootlog.Config{
		SchemaVersion: bootlog.SchemaVersion,
		BootStart:     bootlog.BootStartConfig{Pattern: "^Linux version "},
		Milestones: []bootlog.MilestoneConfig{{
			ID: "application_ready", Pattern: "^APP_READY$", WithinMS: validationInt64(5000),
		}},
		Forbidden: []bootlog.ForbiddenConfig{{ID: "kernel_panic", Pattern: "(?i)kernel panic"}},
	}
}

func TestConfigValidateRejectsInvalidModels(t *testing.T) {
	tests := []struct {
		name  string
		field string
		edit  func(*bootlog.Config)
	}{
		{"missing version", "schema_version", func(c *bootlog.Config) { c.SchemaVersion = 0 }},
		{"unsupported version", "schema_version", func(c *bootlog.Config) { c.SchemaVersion = 2 }},
		{"missing start", "boot_start.pattern", func(c *bootlog.Config) { c.BootStart = bootlog.BootStartConfig{} }},
		{"invalid start regexp", "boot_start.pattern", func(c *bootlog.Config) { c.BootStart.Pattern = "(" }},
		{"empty matching start", "boot_start.pattern", func(c *bootlog.Config) { c.BootStart.Pattern = "^$" }},
		{"no user rules", "rules", func(c *bootlog.Config) { c.Milestones, c.Forbidden = nil, nil }},
		{"empty milestone id", "milestones[0].id", func(c *bootlog.Config) { c.Milestones[0].ID = "" }},
		{"digit first", "milestones[0].id", func(c *bootlog.Config) { c.Milestones[0].ID = "1ready" }},
		{"non ASCII id", "milestones[0].id", func(c *bootlog.Config) { c.Milestones[0].ID = "réady" }},
		{"space in id", "milestones[0].id", func(c *bootlog.Config) { c.Milestones[0].ID = "app ready" }},
		{"long id", "milestones[0].id", func(c *bootlog.Config) { c.Milestones[0].ID = strings.Repeat("a", 65) }},
		{"reserved milestone id", "milestones[0].id", func(c *bootlog.Config) { c.Milestones[0].ID = bootlog.BootStartID }},
		{"reserved forbidden id", "forbidden[0].id", func(c *bootlog.Config) { c.Forbidden[0].ID = bootlog.BootStartID }},
		{"empty milestone pattern", "milestones[0].pattern", func(c *bootlog.Config) { c.Milestones[0].Pattern = "" }},
		{"invalid forbidden regexp", "forbidden[0].pattern", func(c *bootlog.Config) { c.Forbidden[0].Pattern = "[" }},
		{"optional pattern", "milestones[0].pattern", func(c *bootlog.Config) { c.Milestones[0].Pattern = "x?" }},
		{"star pattern", "forbidden[0].pattern", func(c *bootlog.Config) { c.Forbidden[0].Pattern = "(?s).*" }},
		{"empty alternative", "forbidden[0].pattern", func(c *bootlog.Config) { c.Forbidden[0].Pattern = "panic|" }},
		{"invalid UTF-8 pattern", "forbidden[0].pattern", func(c *bootlog.Config) { c.Forbidden[0].Pattern = "bad\xff" }},
		{"pattern byte limit", "milestones[0].pattern", func(c *bootlog.Config) { c.Milestones[0].Pattern = strings.Repeat("é", 2049) }},
		{"negative deadline", "milestones[0].within_ms", func(c *bootlog.Config) { c.Milestones[0].WithinMS = validationInt64(-1) }},
		{"deadline nanosecond overflow", "milestones[0].within_ms", func(c *bootlog.Config) { c.Milestones[0].WithinMS = validationInt64(bootlog.MaxWithinMS + 1) }},
		{"maximum int deadline overflows", "milestones[0].within_ms", func(c *bootlog.Config) { c.Milestones[0].WithinMS = validationInt64(math.MaxInt64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validationConfig()
			test.edit(&config)
			err := config.Validate()
			if !errors.Is(err, bootlog.ErrInvalidRules) {
				t.Fatalf("Validate() = %v, want ErrInvalidRules", err)
			}
			var typed *bootlog.InvalidRulesError
			if !errors.As(err, &typed) || typed.Field != test.field {
				t.Fatalf("Validate() = %#v, want InvalidRulesError field %q", err, test.field)
			}
		})
	}
}

func TestConfigValidateDuplicateIDLocations(t *testing.T) {
	tests := []struct {
		name, duplicate, first string
		edit                   func(*bootlog.Config)
	}{
		{"milestones", "milestones[1].id", "milestones[0].id", func(c *bootlog.Config) {
			c.Milestones = append(c.Milestones, c.Milestones[0])
		}},
		{"forbidden", "forbidden[1].id", "forbidden[0].id", func(c *bootlog.Config) {
			c.Forbidden = append(c.Forbidden, c.Forbidden[0])
		}},
		{"across arrays", "forbidden[0].id", "milestones[1].id", func(c *bootlog.Config) {
			c.Milestones = append(c.Milestones, bootlog.MilestoneConfig{ID: c.Forbidden[0].ID, Pattern: "READY"})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validationConfig()
			test.edit(&config)
			err := config.Validate()
			var typed *bootlog.InvalidRulesError
			if !errors.Is(err, bootlog.ErrInvalidRules) || !errors.As(err, &typed) ||
				typed.Field != test.duplicate || !strings.Contains(typed.Detail, test.first) {
				t.Fatalf("Validate() = %v; want %s to identify its conflict with %s", err, test.duplicate, test.first)
			}
		})
	}
}

func TestConfigValidateAcceptsBoundaries(t *testing.T) {
	tests := []struct {
		name string
		edit func(*bootlog.Config)
	}{
		{"ordinary", func(*bootlog.Config) {}},
		{"presence only", func(c *bootlog.Config) { c.Milestones[0].WithinMS = nil }},
		{"zero deadline", func(c *bootlog.Config) { c.Milestones[0].WithinMS = validationInt64(0) }},
		{"maximum deadline", func(c *bootlog.Config) { c.Milestones[0].WithinMS = validationInt64(bootlog.MaxWithinMS) }},
		{"maximum id", func(c *bootlog.Config) { c.Milestones[0].ID = "A" + strings.Repeat("a", 63) }},
		{"all id punctuation", func(c *bootlog.Config) { c.Milestones[0].ID = "Z0a_-." }},
		{"maximum pattern bytes", func(c *bootlog.Config) { c.Milestones[0].Pattern = strings.Repeat("é", 2048) }},
		{"forbidden only", func(c *bootlog.Config) { c.Milestones = nil }},
		{"milestones only", func(c *bootlog.Config) { c.Forbidden = nil }},
		{"case distinct ids", func(c *bootlog.Config) { c.Milestones[0].ID, c.Forbidden[0].ID = "Ready", "ready" }},
		{"nonempty zero-width context", func(c *bootlog.Config) { c.Milestones[0].Pattern = `\bready\b` }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validationConfig()
			test.edit(&config)
			if err := config.Validate(); err != nil {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}

func TestConfigValidateRuleCountLimit(t *testing.T) {
	for _, milestones := range []int{0, 64, 128} {
		t.Run(fmt.Sprintf("%d milestones", milestones), func(t *testing.T) {
			config := validationConfig()
			config.Milestones, config.Forbidden = nil, nil
			for i := 0; i < milestones; i++ {
				config.Milestones = append(config.Milestones, bootlog.MilestoneConfig{ID: fmt.Sprintf("m%d", i), Pattern: "ready"})
			}
			for i := milestones; i < 128; i++ {
				config.Forbidden = append(config.Forbidden, bootlog.ForbiddenConfig{ID: fmt.Sprintf("f%d", i), Pattern: "panic"})
			}
			if err := config.Validate(); err != nil {
				t.Fatalf("128 user rules plus start rejected: %v", err)
			}
			config.Forbidden = append(config.Forbidden, bootlog.ForbiddenConfig{ID: "extra", Pattern: "panic"})
			if err := config.Validate(); !errors.Is(err, bootlog.ErrInvalidRules) {
				t.Fatalf("129 user rules: got %v, want ErrInvalidRules", err)
			}
		})
	}
}

func TestLogValidateAcceptsStructuralBoundaries(t *testing.T) {
	tests := []struct {
		name string
		log  bootlog.Log
	}{
		{"zero log", bootlog.Log{}},
		{"empty records", bootlog.Log{Records: []bootlog.Record{}}},
		{"untimed control text", bootlog.Log{Records: []bootlog.Record{{Line: 1, Message: " λ\x00\x1b\t\r"}}}},
		{"zero timestamp and blank message", bootlog.Log{Records: []bootlog.Record{{Line: 1, TimestampNS: validationInt64(0)}}}},
		{"maximum values", bootlog.Log{Records: []bootlog.Record{{Line: math.MaxInt64, TimestampNS: validationInt64(math.MaxInt64), Message: "ready"}}}},
		{"gaps equal times and untimed", bootlog.Log{Records: []bootlog.Record{
			{Line: 2, TimestampNS: validationInt64(7), Message: "preamble"},
			{Line: 4, Message: "untimed"},
			{Line: 9, TimestampNS: validationInt64(7), Message: "start"},
			{Line: 10, TimestampNS: validationInt64(8), Message: "ready"},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.log.Validate(); err != nil {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}

func TestLogValidateRejectsInvalidModels(t *testing.T) {
	tests := []struct {
		name    string
		records []bootlog.Record
		field   string
		line    int64
	}{
		{"zero line", []bootlog.Record{{Line: 0}}, "records[0].line", 0},
		{"negative line", []bootlog.Record{{Line: -1}}, "records[0].line", -1},
		{"duplicate line", []bootlog.Record{{Line: 2}, {Line: 2}}, "records[1].line", 2},
		{"decreasing line", []bootlog.Record{{Line: 2}, {Line: 1}}, "records[1].line", 1},
		{"invalid UTF-8", []bootlog.Record{{Line: 5, Message: "\xff"}}, "records[0].message", 5},
		{"negative timestamp", []bootlog.Record{{Line: 1, TimestampNS: validationInt64(-1)}}, "records[0].timestamp_ns", 1},
		{"decreasing timestamp", []bootlog.Record{{Line: 1, TimestampNS: validationInt64(2)}, {Line: 2, TimestampNS: validationInt64(1)}}, "records[1].timestamp_ns", 2},
		{"decrease across untimed", []bootlog.Record{{Line: 1, TimestampNS: validationInt64(2)}, {Line: 2}, {Line: 3, TimestampNS: validationInt64(1)}}, "records[2].timestamp_ns", 3},
		{"negative after untimed", []bootlog.Record{{Line: 1}, {Line: 2, TimestampNS: validationInt64(-1)}}, "records[1].timestamp_ns", 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (bootlog.Log{Records: test.records}).Validate()
			if !errors.Is(err, bootlog.ErrInvalidInput) {
				t.Fatalf("Validate() = %v, want ErrInvalidInput", err)
			}
			var typed *bootlog.InvalidInputError
			if !errors.As(err, &typed) || typed.Field != test.field || typed.Line != test.line {
				t.Fatalf("Validate() = %#v, want InvalidInputError field %q, line %d", err, test.field, test.line)
			}
		})
	}
}

func TestLogValidateDoesNotReapplyParserCaps(t *testing.T) {
	// A caller may have parsed with larger explicit caps or constructed the log.
	largeMessage := strings.Repeat("x", int(bootlog.DefaultMaxInputBytes)+1)
	if err := (bootlog.Log{Records: []bootlog.Record{{Line: 1, Message: largeMessage}}}).Validate(); err != nil {
		t.Fatalf("caller-owned message exceeding default line and input caps rejected: %v", err)
	}
	records := make([]bootlog.Record, int(bootlog.DefaultMaxLines)+1)
	for i := range records {
		records[i] = bootlog.Record{Line: int64(i) + 1, Message: "x"}
	}
	if err := (bootlog.Log{Records: records}).Validate(); err != nil {
		t.Fatalf("caller-owned log exceeding default line cap rejected: %v", err)
	}
}

func TestLogValidateDoesNotAllocatePerRecord(t *testing.T) {
	log := bootlog.Log{Records: make([]bootlog.Record, 1024)}
	timestamp := int64(1)
	for i := range log.Records {
		log.Records[i] = bootlog.Record{Line: int64(i + 1), TimestampNS: &timestamp, Message: "console ready"}
	}
	allocs := testing.AllocsPerRun(5, func() {
		if err := log.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 1 {
		t.Fatalf("validating %d records took %.0f allocations; want at most 1", len(log.Records), allocs)
	}
}
