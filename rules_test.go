package bootlog_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	bootlog "github.com/firmdura/bootlog-check"
	"github.com/firmdura/bootlog-check/internal/testfixture"
)

const rulesMinimalJSON = `{"schema_version":1,"boot_start":{"pattern":"START"},"forbidden":[{"id":"bad","pattern":"BAD"}]}`

func TestParseRulesValid(t *testing.T) {
	cfg, err := bootlog.ParseRules(strings.NewReader(testfixture.RulesJSON()))
	if err != nil || !reflect.DeepEqual(cfg, testfixture.Config()) {
		t.Fatalf("ParseRules = %#v, %v", cfg, err)
	}
	for _, text := range []string{
		rulesMinimalJSON,
		` {"schema_version":1,"boot_start":{"pattern":"START"},"milestones":[{"id":"ready","pattern":"READY","within_ms":0}]} `,
		`{"schema_version":1,"boot_start":{"pattern":"START"},"milestones":[],"forbidden":[{"id":"bad","pattern":"\ud83d\ude00"}]}`,
		strings.Replace(rulesMinimalJSON, "BAD", `\uFFFD`, 1),
	} {
		cfg, err := bootlog.ParseRules(strings.NewReader(text))
		if err != nil {
			t.Fatalf("ParseRules(%s): %v", text, err)
		}
		if _, err := bootlog.CompileRules(cfg); err != nil {
			t.Fatalf("parsed config did not compile: %v", err)
		}
	}
	for _, path := range []string{"testdata/rules/sample.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bootlog.ParseRules(bytes.NewReader(data)); err != nil {
			t.Fatalf("sample %s: %v", path, err)
		}
	}
}

func TestParseRulesSyntaxLocation(t *testing.T) {
	for _, tc := range []struct {
		name, document, location string
		end                      error
	}{
		{"missing comma", "{\n  \"schema_version\": 1\n  \"boot_start\": {}\n}", "line 3, byte column 3", nil},
		{"nested value after UTF-8", "{\r\n  \"boot_start\": {\"pattern\": \"café\"!}\r\n}", "line 2, byte column 36", nil},
		{"trailing punctuation", rulesMinimalJSON + "\r\n  !", "line 2, byte column 3", nil},
		{"unfinished value", "{\n  \"boot_start\": {\n", "line 3, byte column 1", io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := bootlog.ParseRules(strings.NewReader(tc.document))
			if !errors.Is(err, bootlog.ErrInvalidRules) || !strings.Contains(err.Error(), tc.location) {
				t.Fatalf("error = %v, want invalid rules at %s", err, tc.location)
			}
			if !reflect.DeepEqual(cfg, bootlog.Config{}) {
				t.Fatalf("partial config: %#v", cfg)
			}
			var syntax *json.SyntaxError
			if tc.end != nil && !errors.Is(err, tc.end) || tc.end == nil && !errors.As(err, &syntax) {
				t.Fatalf("lost JSON decoder error: %v", err)
			}
		})
	}
}

func TestParseRulesRejectsSyntaxAndSchema(t *testing.T) {
	cases := map[string]string{
		"empty": "", "truncated": `{`, "top null": `null`, "top array": `[]`,
		"trailing document": rulesMinimalJSON + `{}`, "trailing garbage": rulesMinimalJSON + `!`,
		"missing version":       `{"boot_start":{"pattern":"START"},"forbidden":[{"id":"bad","pattern":"BAD"}]}`,
		"missing start":         `{"schema_version":1,"forbidden":[{"id":"bad","pattern":"BAD"}]}`,
		"no rules":              `{"schema_version":1,"boot_start":{"pattern":"START"}}`,
		"empty arrays":          `{"schema_version":1,"boot_start":{"pattern":"START"},"milestones":[],"forbidden":[]}`,
		"missing start pattern": strings.Replace(rulesMinimalJSON, `{"pattern":"START"}`, `{}`, 1),
		"start null":            strings.Replace(rulesMinimalJSON, `{"pattern":"START"}`, `null`, 1),
		"start array":           strings.Replace(rulesMinimalJSON, `{"pattern":"START"}`, `[]`, 1),
		"unknown root":          strings.Replace(rulesMinimalJSON, `"schema_version":1`, `"schema_version":1,"extra":0`, 1),
		"unknown start":         strings.Replace(rulesMinimalJSON, `"pattern":"START"`, `"pattern":"START","extra":0`, 1),
		"unknown forbidden":     strings.Replace(rulesMinimalJSON, `"id":"bad"`, `"id":"bad","within_ms":1`, 1),
		"duplicate root":        strings.Replace(rulesMinimalJSON, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		"duplicate start":       strings.Replace(rulesMinimalJSON, `"pattern":"START"`, `"pattern":"START","pattern":"START"`, 1),
		"decoded duplicate":     strings.Replace(rulesMinimalJSON, `"pattern":"START"`, `"pattern":"START","\u0070attern":"START"`, 1),
		"duplicate forbidden":   strings.Replace(rulesMinimalJSON, `"id":"bad"`, `"id":"bad","id":"bad"`, 1),
		"case variant":          strings.Replace(rulesMinimalJSON, `"pattern"`, `"Pattern"`, 1),
		"invalid UTF8":          strings.Replace(rulesMinimalJSON, "BAD", string([]byte{0xff}), 1),
		"lone high surrogate":   strings.Replace(rulesMinimalJSON, "BAD", `\ud800`, 1),
		"lone low surrogate":    strings.Replace(rulesMinimalJSON, "BAD", `\udfff`, 1),
		"wrong surrogate pair":  strings.Replace(rulesMinimalJSON, "BAD", `\ud800\u0041`, 1),
		"incomplete escape":     strings.Replace(rulesMinimalJSON, "BAD", `\u12`, 1),
		"invalid escape":        strings.Replace(rulesMinimalJSON, "BAD", `\u12zz`, 1),
		"null rule":             strings.Replace(rulesMinimalJSON, `{"id":"bad","pattern":"BAD"}`, `null`, 1),
		"array rule":            strings.Replace(rulesMinimalJSON, `{"id":"bad","pattern":"BAD"}`, `[]`, 1),
		"missing rule ID":       strings.Replace(rulesMinimalJSON, `"id":"bad",`, ``, 1),
		"missing rule pattern":  strings.Replace(rulesMinimalJSON, `,"pattern":"BAD"`, ``, 1),
	}
	for _, field := range []string{"schema_version", "boot_start", "milestones", "forbidden"} {
		for _, value := range []string{"null", "true", `"wrong"`} {
			base := `{"schema_version":1,"boot_start":{"pattern":"START"},"milestones":[{"id":"ready","pattern":"READY"}],"forbidden":[]}`
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(base), &raw); err != nil {
				t.Fatal(err)
			}
			raw[field] = json.RawMessage(value)
			data, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			cases[field+"="+value] = string(data)
		}
	}
	for _, value := range []string{`0`, `2`, `-1`, `1.0`, `1e0`, `9223372036854775808`} {
		cases["version="+value] = strings.Replace(rulesMinimalJSON, `"schema_version":1`, `"schema_version":`+value, 1)
	}
	for _, field := range []string{"id", "pattern"} {
		for _, value := range []string{`null`, `1`, `[]`, `{}`, `false`} {
			cases[field+"="+value] = strings.Replace(rulesMinimalJSON, `"`+field+`":"`+map[string]string{"id": "bad", "pattern": "BAD"}[field]+`"`, `"`+field+`":`+value, 1)
		}
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := bootlog.ParseRules(strings.NewReader(text))
			var typed *bootlog.InvalidRulesError
			if !errors.Is(err, bootlog.ErrInvalidRules) || !errors.As(err, &typed) {
				t.Fatalf("wanted invalid rules, got %#v, %v", cfg, err)
			}
			if !reflect.DeepEqual(cfg, bootlog.Config{}) {
				t.Fatalf("partial config returned: %#v", cfg)
			}
		})
	}
}

func TestParseRulesMilestoneSchema(t *testing.T) {
	const base = `{"schema_version":1,"boot_start":{"pattern":"START"},"milestones":[{"id":"ready","pattern":"READY","within_ms":0}]}`
	for _, raw := range []string{
		`null`, `{}`, `{"id":"ready"}`, `{"pattern":"READY"}`,
		`{"id":"ready","pattern":"READY","extra":1}`,
		`{"id":"ready","id":"ready","pattern":"READY"}`,
		`{"id":"ready","pattern":"READY","within_ms":0,"within_ms":0}`,
	} {
		text := strings.Replace(base, `{"id":"ready","pattern":"READY","within_ms":0}`, raw, 1)
		if _, err := bootlog.ParseRules(strings.NewReader(text)); !errors.Is(err, bootlog.ErrInvalidRules) {
			t.Errorf("milestone %s: %v", raw, err)
		}
	}
	for _, value := range []string{`null`, `-1`, `1.0`, `1e0`, `"1"`, `true`, `[]`, `{}`, `9223372036855`, `9223372036854775808`, `184467440737095516160`} {
		text := strings.Replace(base, `"within_ms":0`, `"within_ms":`+value, 1)
		if _, err := bootlog.ParseRules(strings.NewReader(text)); !errors.Is(err, bootlog.ErrInvalidRules) {
			t.Errorf("within_ms=%s: %v", value, err)
		}
	}
	text := strings.Replace(base, `"within_ms":0`, `"within_ms":9223372036854`, 1)
	cfg, err := bootlog.ParseRules(strings.NewReader(text))
	if err != nil || *cfg.Milestones[0].WithinMS != bootlog.MaxWithinMS {
		t.Fatalf("max deadline: %#v, %v", cfg, err)
	}
}

func TestCompileRulesAndParseRulesValidation(t *testing.T) {
	cases := map[string]func(*bootlog.Config){
		"version":                 func(c *bootlog.Config) { c.SchemaVersion = 2 },
		"no rules":                func(c *bootlog.Config) { c.Milestones = nil; c.Forbidden = nil },
		"empty start":             func(c *bootlog.Config) { c.BootStart.Pattern = "" },
		"invalid start":           func(c *bootlog.Config) { c.BootStart.Pattern = "[" },
		"start matches empty":     func(c *bootlog.Config) { c.BootStart.Pattern = "a*" },
		"duplicate IDs":           func(c *bootlog.Config) { c.Forbidden[0].ID = c.Milestones[0].ID },
		"duplicate milestones":    func(c *bootlog.Config) { c.Milestones[1].ID = c.Milestones[0].ID },
		"duplicate forbidden":     func(c *bootlog.Config) { c.Forbidden = append(c.Forbidden, c.Forbidden[0]) },
		"reserved":                func(c *bootlog.Config) { c.Forbidden[0].ID = "boot_start" },
		"negative deadline":       func(c *bootlog.Config) { c.Milestones[0].WithinMS = testfixture.Int64(-1) },
		"overflow deadline":       func(c *bootlog.Config) { c.Milestones[0].WithinMS = testfixture.Int64(bootlog.MaxWithinMS + 1) },
		"oversize pattern":        func(c *bootlog.Config) { c.Forbidden[0].Pattern = strings.Repeat("x", bootlog.MaxPatternBytes+1) },
		"unicode pattern bytes":   func(c *bootlog.Config) { c.Milestones[0].Pattern = strings.Repeat("é", bootlog.MaxPatternBytes/2+1) },
		"empty milestone pattern": func(c *bootlog.Config) { c.Milestones[0].Pattern = "" },
		"empty forbidden pattern": func(c *bootlog.Config) { c.Forbidden[0].Pattern = "" },
		"invalid milestone regex": func(c *bootlog.Config) { c.Milestones[0].Pattern = "(" },
		"invalid forbidden regex": func(c *bootlog.Config) { c.Forbidden[0].Pattern = "[" },
		"empty match":             func(c *bootlog.Config) { c.Milestones[0].Pattern = "^$" },
		"optional match":          func(c *bootlog.Config) { c.Forbidden[0].Pattern = "x?" },
		"too many rules": func(c *bootlog.Config) {
			for len(c.Milestones)+len(c.Forbidden) <= bootlog.MaxUserRules {
				c.Forbidden = append(c.Forbidden, bootlog.ForbiddenConfig{ID: fmt.Sprintf("f%d", len(c.Forbidden)), Pattern: "BAD"})
			}
		},
	}
	for _, id := range []string{"", "1bad", "bad space", "bad/slash", "é", strings.Repeat("a", 65)} {
		cases["ID "+id] = func(c *bootlog.Config) { c.Milestones[0].ID = id }
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testfixture.Config()
			mutate(&cfg)
			set, err := bootlog.CompileRules(cfg)
			if set != nil || !errors.Is(err, bootlog.ErrInvalidRules) {
				t.Fatalf("CompileRules: %v, %v", set, err)
			}
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bootlog.ParseRules(bytes.NewReader(data)); !errors.Is(err, bootlog.ErrInvalidRules) {
				t.Fatalf("ParseRules: %v", err)
			}
		})
	}
	for _, field := range []string{"start", "milestone", "forbidden"} {
		cfg := testfixture.Config()
		bad := string([]byte{0xff})
		switch field {
		case "start":
			cfg.BootStart.Pattern = bad
		case "milestone":
			cfg.Milestones[0].Pattern = bad
		case "forbidden":
			cfg.Forbidden[0].Pattern = bad
		}
		if _, err := bootlog.CompileRules(cfg); !errors.Is(err, bootlog.ErrInvalidRules) {
			t.Errorf("invalid UTF8 %s: %v", field, err)
		}
	}
	cfg := testfixture.Config()
	cfg.Milestones[0].ID = "A" + strings.Repeat("_.-9", 15) + "abc"
	cfg.Milestones[0].Pattern = strings.Repeat("é", bootlog.MaxPatternBytes/2)
	for len(cfg.Milestones)+len(cfg.Forbidden) < bootlog.MaxUserRules {
		cfg.Forbidden = append(cfg.Forbidden, bootlog.ForbiddenConfig{ID: fmt.Sprintf("f%d", len(cfg.Forbidden)), Pattern: "BAD"})
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bootlog.ParseRules(bytes.NewReader(data)); err != nil {
		t.Fatalf("exact ID, pattern, rule bounds: %v", err)
	}
}

type rulesErrorReader struct {
	data []byte
	err  error
}

func (r *rulesErrorReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) > 0 {
		return n, nil
	}
	return n, r.err
}

type rulesCountReader struct{ count int64 }

func (r *rulesCountReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	r.count += int64(len(p))
	return len(p), nil
}

func TestParseRulesReadBoundsAndErrors(t *testing.T) {
	exact := rulesMinimalJSON + strings.Repeat(" ", int(bootlog.MaxRulesBytes)-len(rulesMinimalJSON))
	if _, err := bootlog.ParseRules(strings.NewReader(exact)); err != nil {
		t.Fatalf("exact byte cap: %v", err)
	}
	cfg, err := bootlog.ParseRules(strings.NewReader(exact + " "))
	var limit *bootlog.LimitError
	if !errors.Is(err, bootlog.ErrLimit) || !errors.As(err, &limit) || limit.Limit != bootlog.MaxRulesBytes || !reflect.DeepEqual(cfg, bootlog.Config{}) {
		t.Fatalf("over byte cap: %#v %v", cfg, err)
	}
	reader := &rulesCountReader{}
	if _, err := bootlog.ParseRules(reader); !errors.Is(err, bootlog.ErrLimit) || reader.count != bootlog.MaxRulesBytes+1 {
		t.Fatalf("bounded read: %d, %v", reader.count, err)
	}
	for _, data := range []string{"", rulesMinimalJSON} {
		cause := errors.New("source failed")
		cfg, err := bootlog.ParseRules(&rulesErrorReader{data: []byte(data), err: cause})
		if !errors.Is(err, cause) || !errors.Is(err, bootlog.ErrInvalidRules) || !reflect.DeepEqual(cfg, bootlog.Config{}) {
			t.Fatalf("reader error: %#v %v", cfg, err)
		}
	}
	if _, err := bootlog.ParseRules(&rulesErrorReader{data: []byte(rulesMinimalJSON), err: io.EOF}); err != nil {
		t.Fatal(err)
	}
	if _, err := bootlog.ParseRules(nil); !errors.Is(err, bootlog.ErrInvalidRules) {
		t.Fatal(err)
	}
}

func FuzzParseRules(f *testing.F) {
	for _, seed := range []string{rulesMinimalJSON, testfixture.RulesJSON(), `null`, `{`, `{"schema_version":1,"schema_version":1}`, strings.Replace(rulesMinimalJSON, "BAD", `\ud800`, 1), rulesMinimalJSON + `[]`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// Bound fuzz work independently of the product's document cap; exact cap
		// behavior has deterministic tests above.
		if len(data) > 16<<10 {
			t.Skip()
		}
		cfg, err := bootlog.ParseRules(bytes.NewReader(data))
		if err != nil {
			if !reflect.DeepEqual(cfg, bootlog.Config{}) {
				t.Fatal("partial config")
			}
			return
		}
		set, err := bootlog.CompileRules(cfg)
		if err != nil || set == nil {
			t.Fatalf("successful parse did not compile: %v", err)
		}
		report, err := bootlog.Evaluate(bootlog.Log{}, set)
		if err != nil {
			t.Fatal(err)
		}
		if err := report.Validate(); err != nil {
			t.Fatal(err)
		}
	})
}
