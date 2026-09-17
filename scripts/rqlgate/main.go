// package: scripts/rqlgate / tool
// type:    tool
// job:     compare rql.schema.json against the Go query implementation — every constraint the
// schema states is probed against DecodeQuery, and an unknown keyword fails the gate
// limits:  reads the schema and the ranke wire decoder; asserts nothing about the spec prose
// (-> scripts/rule-citations.sh for rule ids)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rankegraph/ranke-go"
)

// schemaEnv points the gate at a schema of your own, for working offline or against
// one not published yet.
const schemaEnv = "RANKE_RQL_SCHEMA"

// knownKeywords are the constraint keywords this gate knows how to check. A keyword the
// schema uses and this set omits FAILS: an unchecked constraint is a gap in the gate, and
// the third defect of this class was a minLength nobody thought to look for.
var knownKeywords = map[string]bool{
	"enum": true, "const": true, "required": true, "additionalProperties": true,
	"minimum": true, "minLength": true, "minItems": true, "uniqueItems": true,
	"minProperties": true, "maxProperties": true, "pattern": true,
}

// structuralKeywords carry no constraint of their own: they compose or annotate.
var structuralKeywords = map[string]bool{
	"$schema": true, "$id": true, "$ref": true, "$defs": true, "$comment": true,
	"title": true, "description": true, "type": true, "properties": true,
	"items": true, "allOf": true, "oneOf": true, "anyOf": true, "if": true,
	"then": true, "else": true, "default": true, "examples": true,
}

// goOnly is the documented superset: a value Go admits that the schema does not, listed
// one by one with its reason. Legitimate ONLY where the wire path excludes it, which the
// probes below check rather than take on trust — a wildcard here would make the gate
// unreviewable, and a gate reporting a legitimate value gets switched off.
var goOnly = []supersetEntry{
	{Path: "Output.encoding", Value: "native",
		Why: "in-process only: asks for Go objects in ClaimNative, which no wire form carries"},
	{Path: "Execution.report", Value: "error",
		Why: "in-process only: a Go-side verbosity threshold below info"},
	{Path: "Execution.report", Value: "warn",
		Why: "in-process only: a Go-side verbosity threshold below info"},
}

// supersetEntry is one Go-only enum value and why the wire may exclude it.
type supersetEntry struct {
	Path  string // schema definition and property, e.g. "Output.encoding"
	Value string
	Why   string
}

// enumProbe locates an enum in the schema and says how to put one value on the wire, so
// each admitted value can be offered to the decoder and each rejected value refused.
type enumProbe struct {
	Def, Prop string                // where the enum lives in the schema
	Wire      func(v string) string // a minimal query JSON carrying v at that slot
}

// enumProbes covers every enum the schema states. A schema enum this list omits fails the
// gate, so adding one to the schema is a change the gate demands be accounted for.
var enumProbes = []enumProbe{
	{"Output", "shape", func(v string) string { return outputWire(`"shape":` + q(v)) }},
	{"Output", "detail", func(v string) string { return outputWire(`"detail":` + q(v)) }},
	{"Output", "form", func(v string) string { return outputWire(`"form":` + q(v)) }},
	{"Output", "encoding", func(v string) string { return outputWire(`"encoding":` + q(v)) }},
	{"OutputContent", "overflow", func(v string) string {
		return outputWire(`"content":{"max":16,"overflow":` + q(v) + `}`)
	}},
	{"OrderKey", "compare", func(v string) string {
		return baseWire(`"order":[{"field":"created_at","compare":` + q(v) + `}]`)
	}},
	{"OrderKey", "dir", func(v string) string {
		return baseWire(`"order":[{"field":"created_at","dir":` + q(v) + `}]`)
	}},
	{"PathStep", "dir", func(v string) string {
		return baseWire(`"select":{"branch":"main","path":[{"dir":` + q(v) + `}]}`)
	}},
	{"Execution", "report", func(v string) string {
		return baseWire(`"execution":{"report":` + q(v) + `}`)
	}},
}

// valueProbe is one non-enum constraint: a wire form the schema REFUSES, which the
// decoder must refuse too. Named by the schema location it comes from, so a failure
// says which constraint went unenforced.
type valueProbe struct {
	At   string // schema location, e.g. "Execution.layer.minLength"
	Wire string
	Why  string
}

// valueProbes offers each stated constraint a value the schema rejects. Every entry
// names a keyword in knownKeywords; coverage of the schema's own constraint list is
// checked separately, so a constraint gaining a probe here is not optional.
var valueProbes = []valueProbe{
	{"Select.branch.minLength", baseWire(`"select":{"branch":""}`),
		"an empty branch names no scope (`R-QSCOPE` rejects it outright)"},
	{"Execution.layer.minLength", baseWire(`"execution":{"layer":""}`),
		"an empty layer name pins nothing (`R-QLAYER`)"},
	{"OrderKey.field.minLength", baseWire(`"order":[{"field":""}]`),
		"an empty sort key names no field"},
	{"Where.field.minLength", baseWire(`"where":{"field":"","eq":1}`),
		"an empty comparison field names nothing"},
	{"PathStep.min.minimum", baseWire(`"select":{"branch":"main","path":[{"min":-1}]}`),
		"a negative hop count counts nothing"},
	{"PathStep.max.minimum", baseWire(`"select":{"branch":"main","path":[{"max":-1}]}`),
		"a negative hop bound bounds nothing"},
	{"Limit.results.minimum", baseWire(`"limit":{"results":-1}`),
		"a negative result cap caps nothing"},
	{"OutputContent.max.minimum", outputWire(`"content":{"max":-1,"overflow":"cutoff"}`),
		"a negative content cap caps nothing"},
	{"Query.required", `{}`,
		"select is required: a query with no generator selects nothing"},
	{"Select.required", baseWire(`"select":{}`),
		"branch is required: scope is mandatory (`R-QSCOPE`)"},
	{"Where.oneOf.minItems", baseWire(`"where":{"and":[]}`),
		"an empty and-list is not a filter"},
	{"Select.claim.oneOf.minItems", baseWire(`"select":{"branch":"main","claim":[]}`),
		"an anchor set names at least one claim (`R-QANCHOR`)"},
	{"Select.claim.oneOf.uniqueItems", baseWire(
		`"select":{"branch":"main","claim":["bciqmi5j5hnobbrzqrcqeeodhegnb3o4rozbeh24woow3sxdxbzb2qsi","bciqmi5j5hnobbrzqrcqeeodhegnb3o4rozbeh24woow3sxdxbzb2qsi"]}`),
		"an anchor set holds each id once (`R-QANCHOR`)"},
	{"Comparison.minProperties", baseWire(`"where":{"field":"type"}`),
		"a comparison applies exactly one operator"},
	{"Comparison.maxProperties", baseWire(`"where":{"field":"type","eq":1,"ne":2}`),
		"a comparison applies exactly one operator"},
	{"Query.additionalProperties", baseWire(`"nonsense":1`),
		"the schema fixes the key set, so an unknown key is not a query"},
	{"Select.additionalProperties", baseWire(`"select":{"branch":"main","nonsense":1}`),
		"the schema fixes the key set"},
	{"Id.pattern", baseWire(`"select":{"branch":"main","head":"not-an-id"}`),
		"an id is multibase base32 of a self-describing payload"},
	{"Duration.pattern", baseWire(`"limit":{"time":"soon"}`),
		"a duration is a decimal sequence with unit suffixes"},
	{"Select.allOf.const", baseWire(`"select":{"branch":"$universe"}`),
		"$universe requires head, which the schema states as a conditional required"},
}

// acceptProbes are minimal queries the schema ADMITS, so the gate catches the opposite
// divergence: the decoder demanding what the schema leaves optional. That is defect two
// of the three found by hand — overflow required in the code, optional in the schema.
var acceptProbes = []valueProbe{
	{"OutputContent.required", outputWire(`"content":{"max":16}`),
		"overflow is optional: {max: N} alone is a content cap the schema admits"},
	{"Select.minimal", baseWire(`"select":{"branch":"main"}`),
		"branch alone is a whole query — every other field carries a default"},
}

// q quotes a JSON string value.
func q(v string) string { return `"` + v + `"` }

// baseWire wraps fragments into a query carrying the mandatory select, unless the
// fragment states its own.
func baseWire(fragment string) string {
	if strings.HasPrefix(fragment, `"select"`) || strings.HasPrefix(fragment, `"nonsense"`) {
		if strings.HasPrefix(fragment, `"nonsense"`) {
			return `{"select":{"branch":"main"},` + fragment + `}`
		}
		return `{` + fragment + `}`
	}
	return `{"select":{"branch":"main"},` + fragment + `}`
}

// outputWire is baseWire with the fragment inside output.
func outputWire(fragment string) string { return baseWire(`"output":{` + fragment + `}`) }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "rql schema gate:", err)
		os.Exit(1)
	}
}

func run() error {
	path, raw, err := readSchema()
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	var findings []string
	findings = append(findings, checkKeywords(doc)...)
	findings = append(findings, checkEnums(doc)...)
	findings = append(findings, checkProbes()...)

	if len(findings) > 0 {
		for _, f := range findings {
			fmt.Fprintln(os.Stderr, "  "+f)
		}
		return fmt.Errorf("%d divergence(s) between %s and the Go implementation", len(findings), path)
	}
	fmt.Printf("rql schema gate: %s agrees with the Go query implementation (%d enums, %d refusals, %d admissions probed)\n",
		path, len(enumProbes), len(valueProbes), len(acceptProbes))
	return nil
}

// readSchema resolves the schema, failing rather than passing when it is absent: it
// lands in gitignored docs/papers, which `make verify` fetches before this runs.
func readSchema() (string, []byte, error) {
	for _, p := range []string{os.Getenv(schemaEnv), "docs/papers/spec/rql.schema.json"} {
		if p == "" {
			continue
		}
		if b, err := os.ReadFile(p); err == nil {
			return p, b, nil
		}
	}
	return "", nil, fmt.Errorf("no schema found — run 'make docs', or point %s at a copy", schemaEnv)
}

// checkKeywords walks the schema for constraint keywords this gate cannot check. An
// unknown keyword is a gap in the GATE, reported as such rather than passed over.
func checkKeywords(doc map[string]any) []string {
	seen := map[string]string{} // keyword → where it was first met
	// A schema object's keys are keywords; the keys under properties/$defs are NAMES.
	// Reading the two alike would report every field of the query type as a keyword.
	var walkSchema func(node any, at string)
	named := func(node any, at string) {
		m, ok := node.(map[string]any)
		if !ok {
			return
		}
		for name, sub := range m {
			walkSchema(sub, at+"."+name)
		}
	}
	walkSchema = func(node any, at string) {
		switch v := node.(type) {
		case map[string]any:
			for k, child := range v {
				switch {
				case k == "examples" || k == "default":
					// Values of the type, not schema: descending would read a query's
					// own field names as constraint keywords.
				case k == "properties" || k == "$defs":
					named(child, at+"."+k)
				case structuralKeywords[k]:
					walkSchema(child, at+"."+k)
				case knownKeywords[k]:
					// checked elsewhere; its value carries no further schema
				default:
					if _, ok := seen[k]; !ok {
						seen[k] = at + "." + k
					}
				}
			}
		case []any:
			for i, child := range v {
				walkSchema(child, fmt.Sprintf("%s.%d", at, i))
			}
		}
	}
	walkSchema(doc, "")
	if len(seen) == 0 {
		return nil
	}
	var out []string
	for k, at := range seen {
		out = append(out, fmt.Sprintf("keyword %q (at %s) is used by the schema and this gate cannot check it — teach it, or the constraint goes unenforced", k, strings.TrimPrefix(at, ".")))
	}
	sort.Strings(out)
	return out
}

// checkEnums compares each schema enum against what the decoder admits, both ways: a
// value the schema states must decode, and a Go-only value must be both listed in the
// superset and refused on the wire.
func checkEnums(doc map[string]any) []string {
	var out []string
	covered := map[string]bool{}
	for _, p := range enumProbes {
		key := p.Def + "." + p.Prop
		covered[key] = true
		values := enumAt(doc, p.Def, p.Prop)
		if values == nil {
			out = append(out, fmt.Sprintf("%s: the schema states no enum here, so this probe reads nothing", key))
			continue
		}
		for _, v := range values {
			if _, err := ranke.DecodeQuery([]byte(p.Wire(v))); err != nil {
				out = append(out, fmt.Sprintf("%s: the schema admits %q and the decoder refuses it: %v", key, v, err))
			}
		}
		for _, extra := range goOnly {
			if extra.Path != key {
				continue
			}
			if contains(values, extra.Value) {
				out = append(out, fmt.Sprintf("%s: %q is listed as Go-only but the schema states it — drop the entry", key, extra.Value))
				continue
			}
			// The superset is legitimate only where the wire excludes it.
			if _, err := ranke.DecodeQuery([]byte(p.Wire(extra.Value))); err == nil {
				out = append(out, fmt.Sprintf("%s: %q is Go-only (%s) but the WIRE admits it — the superset must not reach the wire", key, extra.Value, extra.Why))
			}
		}
	}
	for _, key := range schemaEnums(doc) {
		if !covered[key] {
			out = append(out, fmt.Sprintf("%s: the schema states an enum here and no probe covers it", key))
		}
	}
	return out
}

// checkProbes offers each refusal probe a value the schema rejects and each admission
// probe one it accepts.
func checkProbes() []string {
	var out []string
	for _, p := range valueProbes {
		if _, err := ranke.DecodeQuery([]byte(p.Wire)); err == nil {
			out = append(out, fmt.Sprintf("%s: the schema refuses this and the decoder accepts it — %s\n      %s", p.At, p.Why, p.Wire))
		}
	}
	for _, p := range acceptProbes {
		if _, err := ranke.DecodeQuery([]byte(p.Wire)); err != nil {
			out = append(out, fmt.Sprintf("%s: the schema admits this and the decoder refuses it — %s\n      %s\n      %v", p.At, p.Why, p.Wire, err))
		}
	}
	return out
}

// enumAt reads $defs.<def>.properties.<prop>.enum.
func enumAt(doc map[string]any, def, prop string) []string {
	defs, _ := doc["$defs"].(map[string]any)
	d, _ := defs[def].(map[string]any)
	props, _ := d["properties"].(map[string]any)
	p, _ := props[prop].(map[string]any)
	raw, ok := p["enum"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// schemaEnums lists every "<def>.<prop>" the schema puts an enum on, so a new one
// without a probe is reported rather than silently uncovered.
func schemaEnums(doc map[string]any) []string {
	var out []string
	defs, _ := doc["$defs"].(map[string]any)
	for name, d := range defs {
		dm, _ := d.(map[string]any)
		props, _ := dm["properties"].(map[string]any)
		for prop, p := range props {
			pm, _ := p.(map[string]any)
			if _, ok := pm["enum"]; ok {
				out = append(out, name+"."+prop)
			}
		}
	}
	sort.Strings(out)
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
