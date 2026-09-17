// package: ranke / query_codec
// type:    io
// job:     the RQL wire codec — a Query to and from the canonical JSON that ranke-graph's
// rql.schema.json fixes
// limits:  the wire form only; the shape checks a decoded query passes live beside it
// (-> query_validate), and which claims a read returns is the executor's (-> query_default)
package ranke

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// DecodeQuery reads a query from its canonical JSON. An absent field keeps its zero
// value; what a caller's silence becomes is the binding's to decide.
func DecodeQuery(data []byte) (Query, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w wireQuery
	if err := dec.Decode(&w); err != nil {
		return Query{}, Wrap(errDecodeQuery, err)
	}
	q, err := w.query()
	if err != nil {
		return Query{}, err
	}
	if err := ValidateQuery(q); err != nil {
		return Query{}, err
	}
	return q, nil
}

// EncodeQuery renders a query as canonical JSON, omitting every zero-valued field,
// so DecodeQuery returns the query it started as.
func EncodeQuery(q Query) ([]byte, error) {
	if err := ValidateQuery(q); err != nil {
		return nil, err
	}
	out, err := json.Marshal(newWireQuery(q))
	if err != nil {
		return nil, Wrap(errEncodeQuery, err)
	}
	return out, nil
}

// --- the wire shapes ------------------------------------------------------
//
// A parallel set of types rather than tags on Query: an Id interface cannot unmarshal
// itself, omitempty drops a false or 0 operator, and Limit.Time is a duration the wire
// spells as a string. Pointers keep an absent field apart from a zero one.

type wireQuery struct {
	Select    wireSelect     `json:"select"`
	Where     *wireWhere     `json:"where,omitempty"`
	Output    *wireOutput    `json:"output,omitempty"`
	Order     []wireOrderKey `json:"order,omitempty"`
	Limit     *wireLimit     `json:"limit,omitempty"`
	Execution *wireExecution `json:"execution,omitempty"`
}

type wireSelect struct {
	Branch string          `json:"branch"`
	Head   *string         `json:"head,omitempty"`
	Claim  wireAnchor      `json:"claim,omitempty"`
	Path   *[]wirePathStep `json:"path,omitempty"` // a pointer: [] is not absent (`R-QSTEPS`)
}

// wireAnchor is `claim` on the wire: one id, or the set of them `R-QANCHOR` also admits,
// which JSON spells as a string or an array of strings.
type wireAnchor []string

// UnmarshalJSON reads either spelling, so a single id needs no array around it.
func (a *wireAnchor) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*a = wireAnchor{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return WithDetail(ErrQueryAnchorSet, "select.claim is an id or an array of them")
	}
	*a = many
	return nil
}

// MarshalJSON writes one anchor as the id itself, so a query that named one reads back
// as it was written.
func (a wireAnchor) MarshalJSON() ([]byte, error) {
	if len(a) == 1 {
		return json.Marshal(a[0])
	}
	return json.Marshal([]string(a))
}

type wirePathStep struct {
	Edges []string `json:"edges,omitempty"`
	Dir   string   `json:"dir,omitempty"`
	Min   *int     `json:"min,omitempty"`
	Max   *int     `json:"max,omitempty"`
	Nodes []string `json:"nodes,omitempty"`
}

type wireWhere struct {
	And   []wireWhere     `json:"and,omitempty"`
	Or    []wireWhere     `json:"or,omitempty"`
	Not   *wireWhere      `json:"not,omitempty"`
	Field string          `json:"field,omitempty"`
	Test  *wireComparison `json:"test,omitempty"`
}

type wireOutput struct {
	Shape    string       `json:"shape,omitempty"`
	Detail   string       `json:"detail,omitempty"`
	Form     string       `json:"form,omitempty"`
	Content  *wireContent `json:"content,omitempty"`
	Encoding string       `json:"encoding,omitempty"`
}

type wireContent struct {
	Max      int    `json:"max"`
	Overflow string `json:"overflow"`
}

type wireOrderKey struct {
	Field   string `json:"field"`
	Compare string `json:"compare,omitempty"`
	Dir     string `json:"dir,omitempty"`
}

type wireLimit struct {
	Results *int    `json:"results,omitempty"`
	Time    *string `json:"time,omitempty"`
}

type wireExecution struct {
	// Layer is a pointer so an ABSENT layer (the backend chooses) stays distinct from
	// a stated empty one, which pins nothing while asking to (`R-QLAYER`, minLength 1).
	Layer  *string `json:"layer,omitempty"`
	Report string  `json:"report,omitempty"`
}

// wireComparison names the one operator applied, so a false or 0 value survives.
type wireComparison struct {
	Op    string
	Value any
}

// UnmarshalJSON reads the single operator the object holds.
func (c *wireComparison) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Wrap(errDecodeQuery, err)
	}
	if len(raw) != 1 {
		return WithDetail(ErrQueryComparisonForm, strconv.Itoa(len(raw))+" operators set")
	}
	for op, encoded := range raw {
		var value any
		if err := json.Unmarshal(encoded, &value); err != nil {
			return WrapDetail(errDecodeQuery, "where.test."+op, err)
		}
		c.Op, c.Value = op, value
	}
	return nil
}

// MarshalJSON writes the operator as the object's one key.
func (c wireComparison) MarshalJSON() ([]byte, error) {
	out, err := json.Marshal(map[string]any{c.Op: c.Value})
	if err != nil {
		return nil, WrapDetail(errEncodeQuery, "where.test."+c.Op, err)
	}
	return out, nil
}

// --- wire -> Query --------------------------------------------------------

// query maps the decoded wire shapes onto the library's types.
func (w wireQuery) query() (Query, error) {
	q := Query{}
	sel, err := w.Select.selection()
	if err != nil {
		return q, err
	}
	q.Select = sel

	if w.Where != nil {
		where, err := w.Where.where()
		if err != nil {
			return q, err
		}
		q.Where = where
	}
	if w.Output != nil {
		q.Output = w.Output.output()
	}
	for _, key := range w.Order {
		q.Order = append(q.Order, OrderKey{
			Field:   key.Field,
			Compare: Collation(key.Compare),
			Dir:     SortDir(key.Dir),
		})
	}
	if w.Limit != nil {
		limit, err := w.Limit.limit()
		if err != nil {
			return q, err
		}
		q.Limit = limit
	}
	if w.Execution != nil {
		if w.Execution.Layer != nil && strings.TrimSpace(*w.Execution.Layer) == "" {
			return q, WithDetail(ErrQueryLayerName, "execution.layer")
		}
		q.Execution = Execution{Report: ReportLevel(w.Execution.Report)}
		if w.Execution.Layer != nil {
			q.Execution.Layer = *w.Execution.Layer
		}
	}
	if err := w.wireOnlyEnums(); err != nil {
		return q, err
	}
	return q, nil
}

// wireOnlyEnums refuses the three values only a Go caller may set: the native encoding
// asks for Go objects, and report's error and warn are Go-side thresholds. ValidateQuery
// admits all three, so a Go-built query keeps them.
func (w wireQuery) wireOnlyEnums() error {
	if w.Output != nil {
		if err := oneOf("output.encoding", w.Output.Encoding, "", string(ResultJSON), string(ResultCBOR)); err != nil {
			return err
		}
	}
	if w.Execution != nil {
		if err := oneOf("execution.report", w.Execution.Report,
			"", string(ReportInfo), string(ReportDebug), string(ReportTrace)); err != nil {
			return err
		}
	}
	return nil
}

// selection maps the generator, parsing the two ids it may carry.
func (w wireSelect) selection() (Select, error) {
	sel := Select{Branch: w.Branch}
	head, err := parseOptionalId("select.head", w.Head)
	if err != nil {
		return sel, err
	}
	sel.Head = head
	anchors, err := w.Claim.ids()
	if err != nil {
		return sel, err
	}
	sel.Claim = anchors
	// An empty path is a path: it takes no step and returns the frontier, where an
	// absent one returns that frontier's closure (`R-QSTEPS`).
	if w.Path != nil {
		sel.Path = []PathStep{}
		for _, step := range *w.Path {
			sel.Path = append(sel.Path, PathStep{
				Edges: step.Edges,
				Dir:   Direction(step.Dir),
				Min:   step.Min,
				Max:   derefOr(step.Max, 0),
				Nodes: step.Nodes,
			})
		}
	}
	return sel, nil
}

// ids parses the anchors, holding the wire to a set: at least one id, each named once
// (`R-QANCHOR`). A Go caller's repeat is the engine's to read as one claim; a wire one
// is a malformed document.
func (a wireAnchor) ids() ([]Id, error) {
	if a == nil {
		return nil, nil
	}
	if len(a) == 0 {
		return nil, WithDetail(ErrQueryAnchorSet, "select.claim names no id")
	}
	out := make([]Id, 0, len(a))
	seen := map[string]bool{}
	for _, encoded := range a {
		if seen[encoded] {
			return nil, WithDetail(ErrQueryAnchorSet, "select.claim repeats "+encoded)
		}
		seen[encoded] = true
		parsed, err := ParseId(encoded)
		if err != nil {
			return nil, WrapDetail(errDecodeQuery, "select.claim", err)
		}
		out = append(out, parsed)
	}
	return out, nil
}

// where maps one node of the boolean tree, recursing into its subtrees.
func (w wireWhere) where() (*Where, error) {
	out := &Where{Field: w.Field}
	for _, sub := range w.And {
		mapped, err := sub.where()
		if err != nil {
			return nil, err
		}
		out.And = append(out.And, *mapped)
	}
	for _, sub := range w.Or {
		mapped, err := sub.where()
		if err != nil {
			return nil, err
		}
		out.Or = append(out.Or, *mapped)
	}
	if w.Not != nil {
		mapped, err := w.Not.where()
		if err != nil {
			return nil, err
		}
		out.Not = mapped
	}
	if w.Test != nil {
		test, err := w.Test.comparison()
		if err != nil {
			return nil, err
		}
		out.Test = test
	}
	return out, nil
}

// comparison places the wire's one operator on the library's Comparison.
func (c wireComparison) comparison() (*Comparison, error) {
	out := &Comparison{}
	switch c.Op {
	case "eq":
		out.Eq = c.Value
	case "ne":
		out.Ne = c.Value
	case "lt":
		out.Lt = c.Value
	case "le":
		out.Le = c.Value
	case "gt":
		out.Gt = c.Value
	case "ge":
		out.Ge = c.Value
	case "in":
		set, ok := c.Value.([]any)
		if !ok {
			return nil, WithDetail(ErrQueryComparisonForm, "in takes a set")
		}
		out.In = set
	case "glob":
		glob, ok := c.Value.(string)
		if !ok {
			return nil, WithDetail(ErrQueryComparisonForm, "glob takes a string")
		}
		out.Glob = glob
	default:
		return nil, WithDetail(ErrQueryComparisonForm, strconv.Quote(c.Op))
	}
	return out, nil
}

// output maps the shaping axes; each wire axis is one library axis.
func (w wireOutput) output() Output {
	out := Output{
		Shape:    Shape(w.Shape),
		Detail:   Detail(w.Detail),
		Form:     Form(w.Form),
		Encoding: ResultEncoding(w.Encoding),
	}
	if w.Content != nil {
		out.Content = &OutputContent{Max: w.Content.Max, Overflow: Overflow(w.Content.Overflow)}
	}
	return out
}

// limit maps the bounds, parsing the wire's duration string.
func (w wireLimit) limit() (Limit, error) {
	out := Limit{Results: derefOr(w.Results, 0)}
	if w.Time == nil {
		return out, nil
	}
	budget, err := time.ParseDuration(*w.Time)
	if err != nil {
		return out, WrapDetail(errDecodeQuery, "limit.time="+strconv.Quote(*w.Time), err)
	}
	out.Time = budget
	return out, nil
}

// parseOptionalId parses an id the wire may omit, naming the field on failure.
func parseOptionalId(field string, encoded *string) (Id, error) {
	if encoded == nil {
		return nil, nil
	}
	parsed, err := ParseId(*encoded)
	if err != nil {
		return nil, WrapDetail(errDecodeQuery, field, err)
	}
	return parsed, nil
}

// derefOr reads an optional wire scalar, falling back when it is absent.
func derefOr[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}

// --- Query -> wire --------------------------------------------------------

// newWireQuery renders a query into the wire shapes, omitting zero-valued fields.
func newWireQuery(q Query) wireQuery {
	w := wireQuery{Select: newWireSelect(q.Select)}
	if q.Where != nil {
		w.Where = newWireWhere(*q.Where)
	}
	if out := newWireOutput(q.Output); out != nil {
		w.Output = out
	}
	for _, key := range q.Order {
		w.Order = append(w.Order, wireOrderKey{
			Field:   key.Field,
			Compare: string(key.Compare),
			Dir:     string(key.Dir),
		})
	}
	if q.Limit.Results != 0 || q.Limit.Time != 0 {
		limit := wireLimit{}
		if q.Limit.Results != 0 {
			limit.Results = &q.Limit.Results
		}
		if q.Limit.Time != 0 {
			budget := q.Limit.Time.String()
			limit.Time = &budget
		}
		w.Limit = &limit
	}
	if q.Execution.Layer != "" || q.Execution.Report != "" {
		w.Execution = &wireExecution{Report: string(q.Execution.Report)}
		if q.Execution.Layer != "" {
			layer := q.Execution.Layer
			w.Execution.Layer = &layer
		}
	}
	return w
}

// newWireSelect renders the generator, each id in its multibase form.
func newWireSelect(sel Select) wireSelect {
	w := wireSelect{Branch: sel.Branch}
	if sel.Head != nil {
		head := sel.Head.String()
		w.Head = &head
	}
	// A repeat names its claim once (`R-QANCHOR`), and the wire holds a set, so the
	// rendering is where a Go caller's repeat goes.
	seen := map[string]bool{}
	for _, id := range sel.Claim {
		if id == nil || seen[id.String()] {
			continue
		}
		seen[id.String()] = true
		w.Claim = append(w.Claim, id.String())
	}
	if sel.Path != nil {
		steps := make([]wirePathStep, 0, len(sel.Path))
		for _, step := range sel.Path {
			out := wirePathStep{Edges: step.Edges, Dir: string(step.Dir), Min: step.Min, Nodes: step.Nodes}
			if step.Max != 0 {
				out.Max = &step.Max
			}
			steps = append(steps, out)
		}
		w.Path = &steps
	}
	return w
}

// newWireWhere renders one node of the boolean tree.
func newWireWhere(where Where) *wireWhere {
	w := &wireWhere{Field: where.Field}
	for _, sub := range where.And {
		w.And = append(w.And, *newWireWhere(sub))
	}
	for _, sub := range where.Or {
		w.Or = append(w.Or, *newWireWhere(sub))
	}
	if where.Not != nil {
		w.Not = newWireWhere(*where.Not)
	}
	if where.Test != nil {
		w.Test = newWireComparison(*where.Test)
	}
	return w
}

// newWireComparison finds the operator set. ValidateQuery has already held it to
// exactly one, so the first match is it.
func newWireComparison(c Comparison) *wireComparison {
	switch {
	case c.Eq != nil:
		return &wireComparison{Op: "eq", Value: c.Eq}
	case c.Ne != nil:
		return &wireComparison{Op: "ne", Value: c.Ne}
	case c.Lt != nil:
		return &wireComparison{Op: "lt", Value: c.Lt}
	case c.Le != nil:
		return &wireComparison{Op: "le", Value: c.Le}
	case c.Gt != nil:
		return &wireComparison{Op: "gt", Value: c.Gt}
	case c.Ge != nil:
		return &wireComparison{Op: "ge", Value: c.Ge}
	case c.In != nil:
		return &wireComparison{Op: "in", Value: c.In}
	default:
		return &wireComparison{Op: "glob", Value: c.Glob}
	}
}

// newWireOutput renders the shaping axes, nil when none is set. The native encoding
// asks for Go objects, so it drops like an unset value.
func newWireOutput(out Output) *wireOutput {
	encoding := out.Encoding
	if encoding == ResultNative {
		encoding = ""
	}
	if out.Shape == "" && out.Detail == "" && out.Form == "" && out.Content == nil && encoding == "" {
		return nil
	}
	w := &wireOutput{
		Shape:    string(out.Shape),
		Detail:   string(out.Detail),
		Form:     string(out.Form),
		Encoding: string(encoding),
	}
	if out.Content != nil {
		w.Content = &wireContent{Max: out.Content.Max, Overflow: string(out.Content.Overflow)}
	}
	return w
}
