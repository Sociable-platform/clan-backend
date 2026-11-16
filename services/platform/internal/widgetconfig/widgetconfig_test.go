package widgetconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures in testdata/examples are the shared ones from the config v1 contract
// (clan-frontend docs/config-v1/examples), copied unchanged. The Studio and the SDK run the
// same rows, so all three agree on what is valid.

type manifestRow struct {
	File   string  `json:"file"`
	Valid  bool    `json:"valid"`
	Errors []Issue `json:"errors"`
}

func readFile(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "examples", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// issueKeys is the (path, rule) pairs of issues, without the human message.
func issueKeys(issues []Issue) map[Issue]bool {
	out := map[Issue]bool{}
	for _, i := range issues {
		out[Issue{Path: i.Path, Rule: i.Rule}] = true
	}
	return out
}

func TestSharedFixtures(t *testing.T) {
	var manifest struct {
		Cases []manifestRow `json:"cases"`
	}
	if err := json.Unmarshal(readFile(t, "manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) < 10 {
		t.Fatalf("the manifest should have its 10 rows, got %d", len(manifest.Cases))
	}
	for _, row := range manifest.Cases {
		t.Run(row.File, func(t *testing.T) {
			issues := Validate(readFile(t, row.File))
			if (len(issues) == 0) != row.Valid {
				t.Fatalf("valid = %v, want %v; issues: %+v", len(issues) == 0, row.Valid, issues)
			}
			// every listed (path, rule) pair must be reported; extra ones are allowed
			got := issueKeys(issues)
			for _, want := range row.Errors {
				if !got[Issue{Path: want.Path, Rule: want.Rule}] {
					t.Errorf("missing %+v in %+v", want, issues)
				}
			}
		})
	}
}

func TestValidExamplesAreUnderTheSizeLimit(t *testing.T) {
	for _, f := range []string{"valid/arena.json", "valid/farm-minimal.json"} {
		if n := len(readFile(t, f)); n >= MaxBytes {
			t.Fatalf("%s is %d bytes", f, n)
		}
	}
}

// arena returns the valid example as a map, to change one thing at a time.
func arena(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(readFile(t, "valid/arena.json"), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func obj(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		m = m[k].(map[string]any)
	}
	return m
}

func issuesOf(t *testing.T, m map[string]any) []Issue {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return Validate(b)
}

func wantIssue(t *testing.T, issues []Issue, path, rule string) {
	t.Helper()
	if !issueKeys(issues)[Issue{Path: path, Rule: rule}] {
		t.Fatalf("want %s at %q, got %+v", rule, path, issues)
	}
}

func TestRejectsAnUnknownFieldAtAnyDepth(t *testing.T) {
	top := arena(t)
	top["customCss"] = "body{}"
	wantIssue(t, issuesOf(t, top), "", "additionalProperties")

	nested := arena(t)
	obj(nested, "theme", "frame")["glow"] = true
	wantIssue(t, issuesOf(t, nested), "/theme/frame", "additionalProperties")
}

func TestRejectsAWrongSchemaVersion(t *testing.T) {
	m := arena(t)
	m["schemaVersion"] = 2
	wantIssue(t, issuesOf(t, m), "/schemaVersion", "const")
}

func TestRejectsAGameIDThatIsNotALowercaseUUID(t *testing.T) {
	for _, id := range []string{"game_arena_7f3a", "7F3A2C10-5B8E-4D21-9A47-0C6E1B2D3F45", ""} {
		m := arena(t)
		m["game_id"] = id
		wantIssue(t, issuesOf(t, m), "/game_id", "pattern")
	}
}

func TestRejectsMissingSections(t *testing.T) {
	m := arena(t)
	delete(m, "theme")
	delete(m, "chat")
	issues := issuesOf(t, m)
	wantIssue(t, issues, "", "required")
	// one issue for the object, and its message names every missing field
	for _, i := range issues {
		if i.Rule == "required" && i.Path == "" {
			if !strings.Contains(i.Message, `"chat"`) || !strings.Contains(i.Message, `"theme"`) {
				t.Fatalf("message should name both fields: %q", i.Message)
			}
			return
		}
	}
}

func TestMessageNamesEveryUnknownField(t *testing.T) {
	m := arena(t)
	m["zeta"] = 1
	m["alpha"] = 2
	for _, i := range issuesOf(t, m) {
		if i.Rule == "additionalProperties" && i.Path == "" {
			if want := `has the unknown fields "alpha", "zeta"`; i.Message != want {
				t.Fatalf("message %q, want %q", i.Message, want)
			}
			return
		}
	}
	t.Fatal("no additionalProperties issue")
}

func TestRejectsLowercaseAndShortHexColors(t *testing.T) {
	lower := arena(t)
	obj(lower, "theme", "colors")["brand"] = "#80eaff"
	wantIssue(t, issuesOf(t, lower), "/theme/colors/brand", "pattern")

	short := arena(t)
	obj(short, "theme", "frame")["color"] = "#FFF"
	wantIssue(t, issuesOf(t, short), "/theme/frame/color", "pattern")
}

func TestRejectsAFontOutsideTheList(t *testing.T) {
	m := arena(t)
	obj(m, "theme", "fonts", "body")["family"] = "Comic Sans MS"
	wantIssue(t, issuesOf(t, m), "/theme/fonts/body/family", "enum")
}

func TestRejectsATextureThatIsAURL(t *testing.T) {
	m := arena(t)
	obj(m, "theme", "textures", "panel")["asset"] = "https://example.com/tex.png"
	// neither a built-in texture nor a custom-... name
	wantIssue(t, issuesOf(t, m), "/theme/textures/panel/asset", "anyOf")
}

func TestRejectsAnUnknownOrRepeatedBadge(t *testing.T) {
	unknown := arena(t)
	obj(unknown, "clan", "badges")["allowed"] = []any{"star", "skull"}
	// neither a built-in badge nor a custom-... name
	wantIssue(t, issuesOf(t, unknown), "/clan/badges/allowed/1", "anyOf")

	repeated := arena(t)
	obj(repeated, "clan", "badges")["allowed"] = []any{"star", "star"}
	wantIssue(t, issuesOf(t, repeated), "/clan/badges/allowed", "uniqueItems")

	tooMany := arena(t)
	obj(tooMany, "clan", "badges")["allowed"] = []any{"star", "diamond", "peak", "spark", "moon", "cross",
		"custom-a", "custom-b", "custom-c", "custom-d", "custom-e", "custom-f", "custom-g"} // 13, the limit is 12
	wantIssue(t, issuesOf(t, tooMany), "/clan/badges/allowed", "maxItems")
}

func TestRejectsBadLabels(t *testing.T) {
	long41 := strings.Repeat("x", 41)
	for name, text := range map[string]string{
		"a URL with a scheme":       "See https://evil.example",
		"a protocol-relative URL":   "Go //evil.example",
		"a www host":                "Visit www.evil.example",
		"an HTML tag":               "<img src=x>",
		"a closing angle bracket":   "a > b",
		"an empty string":           "",
		"more than 40 characters":   long41,
		"a plain http scheme":       "http:foo",
		"a scheme in capital HTTPS": "see https:x",
	} {
		t.Run(name, func(t *testing.T) {
			m := arena(t)
			obj(m, "texts", "labels")["panelTitle"] = text
			for _, i := range issuesOf(t, m) {
				if i.Path == "/texts/labels/panelTitle" {
					return
				}
			}
			t.Fatalf("%q should be rejected", text)
		})
	}
}

func TestAcceptsGoodLabels(t *testing.T) {
	for name, text := range map[string]string{
		"a placeholder":                "+ Start a {clan}",
		"exactly 40 characters":        strings.Repeat("x", 40),
		"40 letters of 2 bytes each":   strings.Repeat("Ə", 40),
		"40 emoji (4 bytes each)":      strings.Repeat("😀", 40),
		"a single slash and a colon":   "Join / leave: now",
		"the word http without colon":  "http is a protocol",
		"a www without a dot after it": "wwwidget",
	} {
		t.Run(name, func(t *testing.T) {
			m := arena(t)
			obj(m, "texts", "labels")["panelTitle"] = text
			if issues := issuesOf(t, m); len(issues) != 0 {
				t.Fatalf("%q should be accepted, got %+v", text, issues)
			}
		})
	}
}

func TestRejectsATermThatIsTooLong(t *testing.T) {
	m := arena(t)
	obj(m, "texts", "terms")["clan"] = strings.Repeat("x", 21)
	wantIssue(t, issuesOf(t, m), "/texts/terms/clan", "maxLength")
}

func TestRejectsALabelKeyThatIsNotInTheContract(t *testing.T) {
	m := arena(t)
	obj(m, "texts", "labels")["footerLink"] = "Docs"
	wantIssue(t, issuesOf(t, m), "/texts/labels", "additionalProperties")
}

func TestRejectsAConfigWithEverythingDisabled(t *testing.T) {
	m := arena(t)
	obj(m, "friends")["enabled"] = false
	obj(m, "clan")["enabled"] = false
	obj(m, "chat")["enabled"] = false
	wantIssue(t, issuesOf(t, m), "", "anyOf")

	// one enabled tab is enough
	obj(m, "chat")["enabled"] = true
	if issues := issuesOf(t, m); len(issues) != 0 {
		t.Fatalf("one enabled tab should be enough, got %+v", issues)
	}
}

func TestRejectsTheWrongTypeOfValue(t *testing.T) {
	for name, tt := range map[string]struct {
		set        func(m map[string]any)
		path, rule string
	}{
		"a string for a number":        {func(m map[string]any) { obj(m, "clan")["maxMembers"] = "30" }, "/clan/maxMembers", "type"},
		"a decimal for a whole number": {func(m map[string]any) { obj(m, "clan")["maxMembers"] = 30.5 }, "/clan/maxMembers", "type"},
		"a number for a boolean":       {func(m map[string]any) { obj(m, "friends")["enabled"] = 1 }, "/friends/enabled", "type"},
		"null for a section":           {func(m map[string]any) { m["theme"] = nil }, "/theme", "type"},
		"a list for a section":         {func(m map[string]any) { m["layout"] = []any{} }, "/layout", "type"},
		"a number for a label":         {func(m map[string]any) { obj(m, "texts", "labels")["panelTitle"] = 5 }, "/texts/labels/panelTitle", "type"},
	} {
		t.Run(name, func(t *testing.T) {
			m := arena(t)
			tt.set(m)
			wantIssue(t, issuesOf(t, m), tt.path, tt.rule)
		})
	}
}

// clan.tags is gone (TEAM-276 step 2): clans have no tags, and migration 000011 removed it from
// every stored config, so a config that still sends it is refused like any unknown field.
func TestRejectsClanTags(t *testing.T) {
	if _, ok := obj(arena(t), "clan")["tags"]; ok {
		t.Fatal("the examples should no longer have clan.tags; update this test")
	}
	for _, v := range []bool{true, false} {
		m := arena(t)
		obj(m, "clan")["tags"] = v
		issues := issuesOf(t, m)
		wantIssue(t, issues, "/clan", "additionalProperties")
		if len(issues) != 1 || !strings.Contains(issues[0].Message, "tags") {
			t.Fatalf("with clan.tags %v: %+v, want one problem that names tags", v, issues)
		}
	}
}

// A whole number written as 30.0 or 3e1 is still a whole number (as in the SDK's validator).
func TestAcceptsWholeNumbersWrittenInOtherWays(t *testing.T) {
	for _, text := range []string{"30.0", "3e1", "30.00", "0.3e2"} {
		doc := strings.Replace(string(readFile(t, "valid/arena.json")), `"maxMembers": 30`, `"maxMembers": `+text, 1)
		if !strings.Contains(doc, text) {
			t.Fatal("the example no longer has maxMembers: 30; update this test")
		}
		if issues := Validate([]byte(doc)); len(issues) != 0 {
			t.Errorf("maxMembers %s should be accepted, got %+v", text, issues)
		}
	}
}

func TestLimits(t *testing.T) {
	for _, tt := range []struct {
		path               string
		set                func(m map[string]any, v any)
		belowMin, aboveMax any
		min, max           any
	}{
		{"/clan/maxMembers", func(m map[string]any, v any) { obj(m, "clan")["maxMembers"] = v }, 1, 201, 2, 200},
		{"/clan/descriptionMax", func(m map[string]any, v any) { obj(m, "clan")["descriptionMax"] = v }, 19, 301, 20, 300},
		{"/chat/maxMessageLength", func(m map[string]any, v any) { obj(m, "chat")["maxMessageLength"] = v }, 19, 1001, 20, 1000},
		{"/chat/slowModeSeconds", func(m map[string]any, v any) { obj(m, "chat")["slowModeSeconds"] = v }, -1, 31, 0, 30},
		{"/chat/historyLimit", func(m map[string]any, v any) { obj(m, "chat")["historyLimit"] = v }, 0, 51, 1, 50},
		{"/theme/frame/width", func(m map[string]any, v any) { obj(m, "theme", "frame")["width"] = v }, 0, 9, 1, 8},
		{"/theme/panel/opacity", func(m map[string]any, v any) { obj(m, "theme", "panel")["opacity"] = v }, 0.49, 1.01, 0.5, 1},
	} {
		t.Run(tt.path, func(t *testing.T) {
			with := func(v any) []Issue {
				m := arena(t)
				tt.set(m, v)
				return issuesOf(t, m)
			}
			if issues := with(tt.min); len(issues) != 0 {
				t.Errorf("min %v: %+v", tt.min, issues)
			}
			if issues := with(tt.max); len(issues) != 0 {
				t.Errorf("max %v: %+v", tt.max, issues)
			}
			wantIssue(t, with(tt.belowMin), tt.path, "minimum")
			wantIssue(t, with(tt.aboveMax), tt.path, "maximum")
		})
	}
}

func TestOversizedConfigIsRejectedBeforeParsing(t *testing.T) {
	big := []byte(`{"schemaVersion":1,"pad":"` + strings.Repeat("x", MaxBytes) + `"}`)
	issues := Validate(big)
	if len(issues) != 1 || issues[0].Rule != RuleSize || issues[0].Path != "" {
		t.Fatalf("got %+v", issues)
	}
	// exactly at the limit is not oversized (it fails the schema for other reasons)
	exact := []byte(`{"pad":"` + strings.Repeat("x", MaxBytes-len(`{"pad":""}`)) + `"}`)
	if len(exact) != MaxBytes {
		t.Fatalf("test setup: %d bytes", len(exact))
	}
	for _, i := range Validate(exact) {
		if i.Rule == RuleSize {
			t.Fatal("a config of exactly 64 KB is allowed by the size rule")
		}
	}
}

func TestBrokenJSON(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":               ``,
		"only spaces":         `   `,
		"not JSON":            `hello`,
		"cut off":             `{"schemaVersion":1`,
		"two documents":       `{} {}`,
		"data after the JSON": `{"a":1} x`,
		"single quotes":       `{'a':1}`,
		"trailing comma":      `{"a":1,}`,
	} {
		t.Run(name, func(t *testing.T) {
			issues := Validate([]byte(raw))
			if len(issues) != 1 || issues[0].Rule != RuleJSON {
				t.Fatalf("got %+v", issues)
			}
		})
	}
}

func TestOnlyAnObjectIsAConfig(t *testing.T) {
	for _, raw := range []string{`[]`, `null`, `5`, `"text"`, `true`} {
		issues := Validate([]byte(raw))
		if len(issues) == 0 {
			t.Errorf("%s should be rejected", raw)
			continue
		}
		wantIssue(t, issues, "", "type")
	}
}

func TestHugeOrOddNumbersDoNotHang(t *testing.T) {
	for _, n := range []string{"1e999999999", "1" + strings.Repeat("0", 200), "0." + strings.Repeat("0", 200) + "1", "-1e-999999999"} {
		doc := strings.Replace(string(readFile(t, "valid/arena.json")), `"maxMembers": 30`, `"maxMembers": `+n, 1)
		if issues := Validate([]byte(doc)); len(issues) == 0 {
			t.Errorf("maxMembers %.20s... should be rejected", n)
		}
	}
}

func TestEscapesFieldNamesInPaths(t *testing.T) {
	m := arena(t)
	obj(m, "texts", "labels")["a/b~c"] = "x"
	wantIssue(t, issuesOf(t, m), "/texts/labels", "additionalProperties")
}

func TestReportsAtMostFiftyIssuesAndNoRepeats(t *testing.T) {
	m := arena(t)
	for i := 0; i < 200; i++ {
		m["extra"+strings.Repeat("x", i)] = 1
	}
	obj(m, "clan")["maxMembers"] = 0
	issues := Validate(mustMarshal(t, m))
	if len(issues) == 0 || len(issues) > maxIssues {
		t.Fatalf("got %d issues", len(issues))
	}
	seen := map[Issue]bool{}
	for _, i := range issues {
		k := Issue{Path: i.Path, Rule: i.Rule}
		if seen[k] {
			t.Fatalf("%+v reported twice", k)
		}
		seen[k] = true
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSameConfigGivesTheSameIssuesInTheSameOrder(t *testing.T) {
	m := arena(t)
	obj(m, "clan")["maxMembers"] = 999
	obj(m, "chat")["maxMessageLength"] = 5000
	m["customCss"] = "x"
	raw := mustMarshal(t, m)
	first := Validate(raw)
	for i := 0; i < 20; i++ {
		if got := Validate(raw); !equalIssues(got, first) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, got, first)
		}
	}
}

func equalIssues(a, b []Issue) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestIssuesHaveAMessageForPeople(t *testing.T) {
	m := arena(t)
	obj(m, "clan")["maxMembers"] = 500
	for _, i := range issuesOf(t, m) {
		if i.Message == "" {
			t.Fatalf("%+v has no message", i)
		}
	}
}

// ---- the schema loader

func TestNewRefusesAKeywordItDoesNotUnderstand(t *testing.T) {
	for name, schema := range map[string]string{
		"format":            `{"type":"string","format":"email"}`,
		"oneOf":             `{"oneOf":[{"type":"string"}]}`,
		"patternProperties": `{"patternProperties":{"^a":{}}}`,
		"nested":            `{"properties":{"a":{"minProperties":2}}}`,
		"inside defs":       `{"$defs":{"x":{"dependentRequired":{}}}}`,
		"additional schema": `{"additionalProperties":{"type":"string"}}`,
		"true additional":   `{"additionalProperties":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New([]byte(schema)); err == nil {
				t.Fatal("a rule that cannot be checked must not be silently ignored")
			}
		})
	}
}

func TestNewRefusesABrokenSchema(t *testing.T) {
	for name, schema := range map[string]string{
		"not JSON":          `{`,
		"not an object":     `[]`,
		"bad regexp":        `{"pattern":"(?=x)"}`,
		"missing reference": `{"$ref":"#/$defs/nope"}`,
		"outside reference": `{"$ref":"other.json"}`,
		"empty anyOf":       `{"anyOf":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New([]byte(schema)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestSmallSchemaWorks(t *testing.T) {
	v, err := New([]byte(`{
		"type":"object","additionalProperties":false,"required":["n"],
		"properties":{"n":{"type":"integer","minimum":1},"tags":{"type":"array","uniqueItems":true,"items":{"type":"string"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if issues := v.Validate([]byte(`{"n":1,"tags":["a","b"]}`)); len(issues) != 0 {
		t.Fatalf("got %+v", issues)
	}
	for raw, want := range map[string]Issue{
		`{}`:                       {Path: "", Rule: "required"},
		`{"n":0}`:                  {Path: "/n", Rule: "minimum"},
		`{"n":1,"x":2}`:            {Path: "", Rule: "additionalProperties"},
		`{"n":1,"tags":["a","a"]}`: {Path: "/tags", Rule: "uniqueItems"},
		`{"n":1,"tags":["a",5]}`:   {Path: "/tags/1", Rule: "type"},
		`{"n":1,"tags":[1,"a"]}`:   {Path: "/tags/0", Rule: "type"},
	} {
		got := v.Validate([]byte(raw))
		if !issueKeys(got)[want] {
			t.Errorf("%s: want %+v, got %+v", raw, want, got)
		}
	}
}

// The embedded file is the contract. These keep an accidental swap of the file noticed.
func TestEmbeddedSchemaIsConfigV1(t *testing.T) {
	var s struct {
		ID       string         `json:"$id"`
		Required []string       `json:"required"`
		Props    map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(schemaJSON, &s); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(s.ID, "/v1.json") {
		t.Fatalf("$id = %q", s.ID)
	}
	if len(s.Required) != 8 || s.Props["schemaVersion"] == nil {
		t.Fatalf("required = %v", s.Required)
	}
}

// ---- rules added for the TEAM-118 amendment of config v1

func TestMinItems(t *testing.T) {
	v, err := New([]byte(`{"properties":{"list":{"type":"array","minItems":2}}}`))
	if err != nil {
		t.Fatal(err)
	}
	wantIssue(t, v.Validate([]byte(`{"list":["a"]}`)), "/list", "minItems")
	if issues := v.Validate([]byte(`{"list":["a","b"]}`)); len(issues) != 0 {
		t.Fatalf("two items are enough, got %+v", issues)
	}
	if issues := v.Validate([]byte(`{"list":"ab"}`)); len(issues) != 1 || issues[0].Rule != "type" {
		t.Fatalf("minItems does not apply to a string, got %+v", issues)
	}
}

// Role ids must be unique; JSON Schema cannot say "unique by one field", so the validator
// checks it after the schema (config-v1 README, Limits). An empty schema shows it on its own.
func TestRejectsARepeatedRoleID(t *testing.T) {
	v, err := New([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	issues := v.Validate([]byte(`{"clan":{"roles":[{"id":"member"},{"id":"elder"},{"id":"member"},{"id":"member"}]}}`))
	wantIssue(t, issues, "/clan/roles/2/id", RuleUniqueRoleIDs)
	wantIssue(t, issues, "/clan/roles/3/id", RuleUniqueRoleIDs)
	if len(issues) != 2 {
		t.Fatalf("want only the two repeats, got %+v", issues)
	}
	// odd shapes are the schema's business, not a crash here
	for _, doc := range []string{`{"clan":{"roles":"x"}}`, `{"clan":[1]}`, `{"clan":{"roles":[1,{"id":2},{}]}}`, `[]`} {
		if issues := v.Validate([]byte(doc)); len(issues) != 0 {
			t.Fatalf("%s: %+v", doc, issues)
		}
	}
}

func TestArenaRolesNeedAtLeastTwo(t *testing.T) {
	m := arena(t)
	clan := obj(m, "clan")
	clan["roles"] = clan["roles"].([]any)[:1]
	wantIssue(t, issuesOf(t, m), "/clan/roles", "minItems")
}

func TestArenaRejectsARepeatedRoleID(t *testing.T) {
	m := arena(t)
	roles := obj(m, "clan")["roles"].([]any)
	roles[1].(map[string]any)["id"] = roles[0].(map[string]any)["id"]
	wantIssue(t, issuesOf(t, m), "/clan/roles/1/id", RuleUniqueRoleIDs)
}
