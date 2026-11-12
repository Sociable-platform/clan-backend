// Package widgetconfig checks a widget config against the config v1 contract.
//
// The contract is config.v1.schema.json, written by the Studio/SDK side and copied here
// unchanged. This package does not repeat its rules in Go. It embeds the file and checks
// configs against it, so a new version of the file is a file replacement, not a code change.
//
// Only the JSON Schema keywords the contract uses are understood (see supportedKeywords).
// A schema that uses any other keyword is refused when the package loads, so a rule can
// never be silently ignored.
//
// Problems are reported as Issue{Path, Rule}: Path is a JSON Pointer to the value ("" is the
// whole document) and Rule is the JSON Schema keyword that failed. This is the same shape
// the SDK and Studio report, so all three agree on examples/manifest.json.
package widgetconfig

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxBytes is the largest config accepted: 64 KB of JSON text. JSON Schema cannot express a
// size limit, so it is checked here, before the config is even parsed.
const MaxBytes = 64 * 1024

// maxIssues caps how many problems one config reports, so a huge broken config cannot
// produce a huge answer.
const maxIssues = 50

// maxNumberText is the longest number literal compared exactly. Longer ones are out of range.
const maxNumberText = 64

// Rules that are not JSON Schema keywords: they report a config that cannot be checked
// against the schema at all.
const (
	RuleSize = "size" // more than MaxBytes
	RuleJSON = "json" // not valid JSON
)

// Issue is one problem found in a config.
type Issue struct {
	Path    string `json:"path"`              // JSON Pointer, "" for the whole document
	Rule    string `json:"rule"`              // JSON Schema keyword (or RuleSize / RuleJSON)
	Message string `json:"message,omitempty"` // for people; clients should rely on Path and Rule
}

//go:embed config.v1.schema.json
var schemaJSON []byte

// current is the validator for the embedded schema. A broken embedded file is a build
// mistake, so it stops the program at start instead of failing on every request.
var current = mustNew(schemaJSON)

// Validate checks raw (the config as JSON text) and returns its problems, or nil when it is
// valid. The order is: size, then JSON syntax, then the schema.
func Validate(raw []byte) []Issue { return current.Validate(raw) }

// Validator checks configs against one schema.
type Validator struct {
	root     map[string]any
	patterns map[string]*regexp.Regexp
}

// supportedKeywords are the JSON Schema keywords this package can check.
var supportedKeywords = map[string]bool{
	"type": true, "const": true, "enum": true, "pattern": true, "not": true,
	"minLength": true, "maxLength": true, "minimum": true, "maximum": true,
	"uniqueItems": true, "minItems": true, "maxItems": true, "items": true, "properties": true,
	"required": true, "additionalProperties": true, "anyOf": true, "$ref": true,
}

// annotations are keywords that describe the schema and never fail a config.
var annotations = map[string]bool{
	"$schema": true, "$id": true, "title": true, "description": true, "$comment": true, "$defs": true,
}

// New builds a Validator from a JSON Schema. It fails when the schema uses a keyword this
// package does not understand, or has a pattern or reference that cannot be used.
func New(schema []byte) (*Validator, error) {
	root, err := decode(schema)
	if err != nil {
		return nil, fmt.Errorf("widgetconfig: schema is not valid JSON: %w", err)
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New("widgetconfig: schema must be a JSON object")
	}
	v := &Validator{root: obj, patterns: map[string]*regexp.Regexp{}}
	if err := v.prepare(obj, "#"); err != nil {
		return nil, err
	}
	return v, nil
}

func mustNew(schema []byte) *Validator {
	v, err := New(schema)
	if err != nil {
		panic(err)
	}
	return v
}

// prepare walks the schema once: it refuses unknown keywords and compiles every pattern.
func (v *Validator) prepare(schema map[string]any, where string) error {
	for key, val := range schema {
		switch {
		case annotations[key] && key != "$defs":
			// nothing to check
		case key == "$defs":
			defs, ok := val.(map[string]any)
			if !ok {
				return fmt.Errorf("widgetconfig: %s/$defs must be an object", where)
			}
			for name, d := range defs {
				if err := v.prepareChild(d, where+"/$defs/"+name); err != nil {
					return err
				}
			}
		case !supportedKeywords[key]:
			return fmt.Errorf("widgetconfig: %s uses the keyword %q, which is not supported", where, key)
		case key == "properties":
			props, ok := val.(map[string]any)
			if !ok {
				return fmt.Errorf("widgetconfig: %s/properties must be an object", where)
			}
			for name, p := range props {
				if err := v.prepareChild(p, where+"/properties/"+name); err != nil {
					return err
				}
			}
		case key == "items" || key == "not":
			if err := v.prepareChild(val, where+"/"+key); err != nil {
				return err
			}
		case key == "anyOf":
			list, ok := val.([]any)
			if !ok || len(list) == 0 {
				return fmt.Errorf("widgetconfig: %s/anyOf must be a non-empty list", where)
			}
			for i, s := range list {
				if err := v.prepareChild(s, fmt.Sprintf("%s/anyOf/%d", where, i)); err != nil {
					return err
				}
			}
		case key == "additionalProperties":
			if b, ok := val.(bool); !ok || b {
				return fmt.Errorf("widgetconfig: %s/additionalProperties: only false is supported", where)
			}
		case key == "pattern":
			text, ok := val.(string)
			if !ok {
				return fmt.Errorf("widgetconfig: %s/pattern must be a string", where)
			}
			re, err := regexp.Compile(text) // RE2: no lookahead, so Go and JS agree
			if err != nil {
				return fmt.Errorf("widgetconfig: %s/pattern %q: %w", where, text, err)
			}
			v.patterns[text] = re
		case key == "$ref":
			ref, ok := val.(string)
			if !ok {
				return fmt.Errorf("widgetconfig: %s/$ref must be a string", where)
			}
			if _, err := v.resolve(ref); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *Validator) prepareChild(child any, where string) error {
	obj, ok := child.(map[string]any)
	if !ok {
		return fmt.Errorf("widgetconfig: %s must be a schema object", where)
	}
	return v.prepare(obj, where)
}

// resolve follows a reference that points inside the schema ("#/$defs/theme").
func (v *Validator) resolve(ref string) (map[string]any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("widgetconfig: reference %q must start with #/", ref)
	}
	var cur any = v.root
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("widgetconfig: reference %q does not exist", ref)
		}
		if cur, ok = obj[part]; !ok {
			return nil, fmt.Errorf("widgetconfig: reference %q does not exist", ref)
		}
	}
	target, ok := cur.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("widgetconfig: reference %q is not a schema", ref)
	}
	return target, nil
}

// Validate checks raw and returns its problems, or nil when it is valid.
func (v *Validator) Validate(raw []byte) []Issue {
	if len(raw) > MaxBytes {
		return []Issue{{Path: "", Rule: RuleSize, Message: fmt.Sprintf("Config must be at most %d KB", MaxBytes/1024)}}
	}
	doc, err := decode(raw)
	if err != nil {
		return []Issue{{Path: "", Rule: RuleJSON, Message: "Config must be valid JSON"}}
	}
	c := &collector{}
	v.check(v.root, doc, "", c)
	checkRoleIDs(doc, c)
	return c.issues
}

// RuleUniqueRoleIDs reports a role id used twice in clan.roles. JSON Schema cannot say
// "unique by one field", so the contract leaves it to the validator (config-v1 README, Limits).
const RuleUniqueRoleIDs = "uniqueRoleIds"

// checkRoleIDs reports every role in clan.roles whose id an earlier role already has.
// Anything that is not the expected shape is skipped: the schema reports that already.
func checkRoleIDs(doc any, c *collector) {
	root, _ := doc.(map[string]any)
	clan, _ := root["clan"].(map[string]any)
	roles, _ := clan["roles"].([]any)
	seen := map[string]bool{}
	for i, r := range roles {
		role, _ := r.(map[string]any)
		id, ok := role["id"].(string)
		if !ok {
			continue
		}
		if seen[id] {
			c.add(fmt.Sprintf("/clan/roles/%d/id", i), RuleUniqueRoleIDs, "repeats the id of another role")
		}
		seen[id] = true
	}
}

// decode reads exactly one JSON value. Numbers stay as json.Number so that 0.5 and 1e2 are
// compared exactly, not as floats.
func decode(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("data after the JSON value")
	}
	return doc, nil
}

// collector gathers issues, without repeats and up to maxIssues.
type collector struct {
	issues []Issue
	seen   map[[2]string]bool
}

func (c *collector) add(path, rule, message string) {
	key := [2]string{path, rule}
	if c.seen == nil {
		c.seen = map[[2]string]bool{}
	}
	if c.seen[key] || len(c.issues) >= maxIssues {
		return
	}
	c.seen[key] = true
	c.issues = append(c.issues, Issue{Path: path, Rule: rule, Message: message})
}

// check applies every keyword of schema to inst. Keywords that do not apply to the kind of
// value (minimum on a string, properties on a list) are skipped, as JSON Schema says.
func (v *Validator) check(schema map[string]any, inst any, path string, c *collector) {
	if ref, ok := schema["$ref"].(string); ok {
		target, _ := v.resolve(ref) // checked in New
		v.check(target, inst, path, c)
	}

	keys := make([]string, 0, len(schema))
	for k := range schema {
		keys = append(keys, k)
	}
	sort.Strings(keys) // the same order every time

	for _, key := range keys {
		val := schema[key]
		switch key {
		case "type":
			v.checkType(val, inst, path, c)
		case "const":
			if !equal(val, inst) {
				c.add(path, "const", "must be "+show(val))
			}
		case "enum":
			list, _ := val.([]any)
			found := false
			for _, option := range list {
				if equal(option, inst) {
					found = true
					break
				}
			}
			if !found {
				c.add(path, "enum", "must be one of the allowed values")
			}
		case "pattern":
			if s, ok := inst.(string); ok && !v.patterns[val.(string)].MatchString(s) {
				c.add(path, "pattern", "has characters or a form that is not allowed")
			}
		case "not":
			var inner collector
			v.check(val.(map[string]any), inst, path, &inner)
			if len(inner.issues) == 0 {
				c.add(path, "not", "contains something that is not allowed (a link or markup)")
			}
		case "minLength":
			if s, ok := inst.(string); ok && float64(utf8.RuneCountInString(s)) < toFloat(val) {
				c.add(path, "minLength", fmt.Sprintf("must be at least %s characters", showNumber(val)))
			}
		case "maxLength":
			if s, ok := inst.(string); ok && float64(utf8.RuneCountInString(s)) > toFloat(val) {
				c.add(path, "maxLength", fmt.Sprintf("must be at most %s characters", showNumber(val)))
			}
		case "minimum":
			if n, ok := inst.(json.Number); ok && !atLeast(n, val.(json.Number)) {
				c.add(path, "minimum", "must be at least "+showNumber(val))
			}
		case "maximum":
			if n, ok := inst.(json.Number); ok && !atMost(n, val.(json.Number)) {
				c.add(path, "maximum", "must be at most "+showNumber(val))
			}
		case "uniqueItems":
			if list, ok := inst.([]any); ok && val == true && hasDuplicates(list) {
				c.add(path, "uniqueItems", "must not repeat a value")
			}
		case "minItems":
			if list, ok := inst.([]any); ok && float64(len(list)) < toFloat(val) {
				c.add(path, "minItems", fmt.Sprintf("must have at least %s items", showNumber(val)))
			}
		case "maxItems":
			if list, ok := inst.([]any); ok && float64(len(list)) > toFloat(val) {
				c.add(path, "maxItems", fmt.Sprintf("must have at most %s items", showNumber(val)))
			}
		case "items":
			if list, ok := inst.([]any); ok {
				for i, item := range list {
					v.check(val.(map[string]any), item, fmt.Sprintf("%s/%d", path, i), c)
				}
			}
		case "required":
			if obj, ok := inst.(map[string]any); ok {
				names, _ := val.([]any)
				var missing []string
				for _, n := range names {
					if name, _ := n.(string); name != "" {
						if _, present := obj[name]; !present {
							missing = append(missing, name)
						}
					}
				}
				if len(missing) > 0 {
					c.add(path, "required", "is missing "+fieldList(missing))
				}
			}
		case "properties":
			if obj, ok := inst.(map[string]any); ok {
				props := val.(map[string]any)
				names := make([]string, 0, len(obj))
				for name := range obj {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					if sub, known := props[name]; known {
						v.check(sub.(map[string]any), obj[name], path+"/"+escape(name), c)
					}
				}
			}
		case "additionalProperties": // only false is supported (checked in New)
			if obj, ok := inst.(map[string]any); ok {
				props, _ := schema["properties"].(map[string]any)
				names := make([]string, 0, len(obj))
				for name := range obj {
					names = append(names, name)
				}
				sort.Strings(names)
				var unknown []string
				for _, name := range names {
					if _, known := props[name]; !known {
						unknown = append(unknown, name)
					}
				}
				if len(unknown) > 0 {
					c.add(path, "additionalProperties", "has the unknown "+fieldList(unknown))
				}
			}
		case "anyOf":
			list := val.([]any)
			matched := false
			for _, option := range list {
				var inner collector
				v.check(option.(map[string]any), inst, path, &inner)
				if len(inner.issues) == 0 {
					matched = true
					break
				}
			}
			if !matched {
				c.add(path, "anyOf", "does not match any of the allowed forms")
			}
		}
	}
}

func (v *Validator) checkType(want any, inst any, path string, c *collector) {
	var allowed []string
	switch t := want.(type) {
	case string:
		allowed = []string{t}
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				allowed = append(allowed, s)
			}
		}
	}
	for _, a := range allowed {
		if isType(inst, a) {
			return
		}
	}
	c.add(path, "type", "must be of type "+strings.Join(allowed, " or "))
}

func isType(inst any, name string) bool {
	switch name {
	case "object":
		_, ok := inst.(map[string]any)
		return ok
	case "array":
		_, ok := inst.([]any)
		return ok
	case "string":
		_, ok := inst.(string)
		return ok
	case "boolean":
		_, ok := inst.(bool)
		return ok
	case "null":
		return inst == nil
	case "number":
		n, ok := inst.(json.Number)
		if !ok {
			return false
		}
		_, exact := toRat(n)
		return exact
	case "integer":
		n, ok := inst.(json.Number)
		if !ok {
			return false
		}
		r, exact := toRat(n)
		return exact && r.IsInt()
	}
	return false
}

// toRat reads a number exactly. A literal that is too long to compare is reported as not
// exact, which makes the config invalid rather than risking a huge calculation.
func toRat(n json.Number) (*big.Rat, bool) {
	text := n.String()
	if len(text) > maxNumberText {
		return nil, false
	}
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		exp := text[i+1:]
		digits := strings.TrimLeft(exp, "+-")
		if len(digits) > 3 { // an exponent over 999 is never a sensible config value
			return nil, false
		}
	}
	r, ok := new(big.Rat).SetString(text)
	return r, ok
}

func atLeast(n, min json.Number) bool {
	a, ok := toRat(n)
	b, _ := toRat(min)
	return ok && a.Cmp(b) >= 0
}

func atMost(n, max json.Number) bool {
	a, ok := toRat(n)
	b, _ := toRat(max)
	return ok && a.Cmp(b) <= 0
}

// equal compares two JSON values: numbers by value (1 and 1.0 are equal), the rest by content.
func equal(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		rx, okx := toRat(x)
		ry, oky := toRat(y)
		return okx && oky && rx.Cmp(ry) == 0
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case nil:
		return b == nil
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, present := y[k]
			if !present || !equal(xv, yv) {
				return false
			}
		}
		return true
	}
	return false
}

func hasDuplicates(list []any) bool {
	for i := range list {
		for j := i + 1; j < len(list); j++ {
			if equal(list[i], list[j]) {
				return true
			}
		}
	}
	return false
}

// fieldList writes names for a message: `field "a"` or `fields "a", "b"` (at most five shown).
func fieldList(names []string) string {
	shown := names
	more := ""
	if len(shown) > 5 {
		shown, more = shown[:5], fmt.Sprintf(" and %d more", len(names)-5)
	}
	quoted := make([]string, len(shown))
	for i, n := range shown {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	word := "field"
	if len(names) > 1 {
		word = "fields"
	}
	return word + " " + strings.Join(quoted, ", ") + more
}

// escape writes a field name as one JSON Pointer step ("~" and "/" have special meaning).
func escape(name string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(name)
}

// toFloat reads a limit from the schema (a small whole number).
func toFloat(v any) float64 {
	n, _ := v.(json.Number)
	f, _ := n.Float64()
	return f
}

func showNumber(v any) string {
	n, _ := v.(json.Number)
	return n.String()
}

func show(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
