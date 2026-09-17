// package: ranke / query_validate
// type:    logic
// job:     the shape checks a query must pass before an engine sees it — the schema's rules plus
// the one it cannot state, a step's Min against its Max
// limits:  shape only, and never which claims a read returns (-> query_default); the wire form
// those rules are stated against is the codec's (-> query_codec)
package ranke

import (
	"strconv"
	"strings"
)

// ValidateQuery holds a query to the schema's rules plus the one it cannot state, a
// step's Min against its Max. Wire and Go callers share it, so both reach one verdict.
func ValidateQuery(q Query) error {
	if q.Select.Branch == "" {
		return ErrQueryNoScope
	}
	if q.Select.Branch == BranchUniverse && q.Select.Head == nil {
		return ErrQueryNoHead
	}
	for i, step := range q.Select.Path {
		if err := validateStep(step); err != nil {
			return WithDetail(err, "select.path["+strconv.Itoa(i)+"]")
		}
	}
	if q.Where != nil {
		if err := validateWhere(*q.Where); err != nil {
			return err
		}
	}
	if err := validateOutput(q.Output); err != nil {
		return err
	}
	// A cap is a count, which the schema bounds at zero. Zero is the unbounded read.
	if q.Limit.Results < 0 {
		return WithDetail(ErrQueryBounds, "limit.results "+strconv.Itoa(q.Limit.Results))
	}
	if q.Limit.Time < 0 {
		return WithDetail(ErrQueryBounds, "limit.time "+q.Limit.Time.String())
	}
	for i, key := range q.Order {
		// A sort key names the field it orders on; an empty one names nothing.
		if key.Field == "" {
			return WithDetail(ErrQueryOrderField, "order["+strconv.Itoa(i)+"]")
		}
		if err := oneOf("order.compare", string(key.Compare), "", string(CompareNumeric), string(CompareLexical), string(CompareTemporal)); err != nil {
			return err
		}
		if err := oneOf("order.dir", string(key.Dir), "", string(SortAsc), string(SortDesc)); err != nil {
			return err
		}
	}
	// A Go caller's empty Layer is an ABSENT one; whitespace states a name and gives
	// none. The wire tells absent from empty and refuses the latter (`R-QLAYER`).
	if q.Execution.Layer != "" && strings.TrimSpace(q.Execution.Layer) == "" {
		return WithDetail(ErrQueryLayerName, "execution.layer")
	}
	return oneOf("execution.report", string(q.Execution.Report), "",
		string(ReportError), string(ReportWarn), string(ReportInfo), string(ReportDebug), string(ReportTrace))
}

// validateStep checks dir and hops. Max 0 is unbounded, so only a bounded Max can sit under Min.
func validateStep(step PathStep) error {
	if err := oneOf("dir", string(step.Dir), "", string(DirProvenance), string(DirUses), string(DirConnections)); err != nil {
		return err
	}
	// A hop count is a count, which the schema bounds at zero on both ends. A negative
	// one breaks that bound as well as the step rule, so it matches ErrQueryBounds too:
	// a caller watching traversals reads ErrQueryHops, one checking every schema minimum
	// reads ErrQueryBounds, and neither has to know the other's sentinel.
	hopBound := alsoMatches(ErrQueryHops, ErrQueryBounds)
	if step.Min != nil && *step.Min < 0 {
		return WithDetail(hopBound, "min "+strconv.Itoa(*step.Min)+" is negative")
	}
	if step.Max < 0 {
		return WithDetail(hopBound, "max "+strconv.Itoa(step.Max)+" is negative")
	}
	// A floor above a bounded ceiling breaks no bound, so it stays the step rule alone.
	if step.Max > 0 && step.MinHops() > step.Max {
		return WithDetail(ErrQueryHops, strconv.Itoa(step.MinHops())+" > "+strconv.Itoa(step.Max))
	}
	return nil
}

// validateWhere holds every node of the tree to exactly one form.
func validateWhere(w Where) error {
	forms := 0
	if len(w.And) > 0 {
		forms++
	}
	if len(w.Or) > 0 {
		forms++
	}
	if w.Not != nil {
		forms++
	}
	if w.Field != "" || w.Test != nil {
		forms++
	}
	if forms != 1 {
		return WithDetail(ErrQueryWhereForm, strconv.Itoa(forms)+" forms set")
	}
	if w.Field != "" || w.Test != nil {
		if w.Field == "" || w.Test == nil {
			return WithDetail(ErrQueryWhereForm, "a leaf carries both a field and a test")
		}
		return validateComparison(w.Field, *w.Test)
	}
	for _, sub := range append(append([]Where{}, w.And...), w.Or...) {
		if err := validateWhere(sub); err != nil {
			return err
		}
	}
	if w.Not != nil {
		return validateWhere(*w.Not)
	}
	return nil
}

// validateComparison holds a comparison to one operator. An explicit empty `in` set
// counts, being present.
func validateComparison(field string, c Comparison) error {
	ops := 0
	for _, set := range []bool{
		c.Eq != nil, c.Ne != nil, c.Lt != nil, c.Le != nil,
		c.Gt != nil, c.Ge != nil, c.In != nil, c.Glob != "",
	} {
		if set {
			ops++
		}
	}
	if ops != 1 {
		return WithDetail(ErrQueryComparisonForm, strconv.Itoa(ops)+" operators set")
	}
	check := timeOperandCheck(field)
	if check == nil {
		return nil
	}
	for _, v := range append([]any{c.Eq, c.Ne, c.Lt, c.Le, c.Gt, c.Ge}, c.In...) {
		if v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return WithDetail(ErrQueryTimeOperand, field+" is "+toStringValue(v))
		}
		if err := check(s); err != nil {
			return WithDetail(ErrQueryTimeOperand, field+"="+s)
		}
	}
	if c.Glob != "" {
		// A pattern names no time, being neither form.
		return WithDetail(ErrQueryTimeOperand, field+" glob "+c.Glob)
	}
	return nil
}

// timeOperandCheck returns the form `R-QTIMEOP` holds a comparison on field to, or
// nil where no time rule governs it. The FIELD picks which of the two forms applies:
// EDTF admits `2026-01-01T00:00:02Z` as readily as the fixed-width spelling, so
// allowing either form on a `V-TIME` field would leave one instant with several
// spellings — and a text comparison lands on a different second for each.
func timeOperandCheck(field string) func(string) error {
	switch field {
	case "created_at", FieldDeleteBy, FieldPubkeyValidFrom, FieldPubkeyExpiresAfter:
		return func(s string) error { _, err := parseRFC3339Nano(s); return err }
	case "dated":
		return validateDated
	default:
		return nil
	}
}

// validateOutput checks each output axis against the values the schema fixes.
func validateOutput(o Output) error {
	if err := oneOf("output.shape", string(o.Shape), "", string(ShapeSingle), string(ShapePath)); err != nil {
		return err
	}
	if err := oneOf("output.detail", string(o.Detail), "", string(DetailID), string(DetailClaims), string(DetailEnvelope)); err != nil {
		return err
	}
	if err := oneOf("output.form", string(o.Form), "", string(FormOriginal), string(FormMaterialized)); err != nil {
		return err
	}
	if err := oneOf("output.encoding", string(o.Encoding), "", string(ResultNative), string(ResultJSON), string(ResultCBOR)); err != nil {
		return err
	}
	if err := validateEnvelopeOutput(o); err != nil {
		return err
	}
	if o.Content == nil {
		return nil
	}
	// A byte cap is a count, which the schema bounds at zero.
	if o.Content.Max < 0 {
		return WithDetail(ErrQueryBounds, "output.content.max "+strconv.Itoa(o.Content.Max))
	}
	// An absent overflow is omit (`R-QCONTENT`), so the pair needs only its cap.
	return oneOf("output.content.overflow", string(o.Content.Overflow),
		"", string(OverflowCutoff), string(OverflowOmit))
}

// validateEnvelopeOutput refuses the axes an envelope cannot answer for
// (`R-QDETAIL`). The bytes are the stored ones, so a resolved overlay is not among
// them and JSON is not what they are; both requests ask for something else under the
// name of the original.
func validateEnvelopeOutput(o Output) error {
	if o.Detail != DetailEnvelope {
		return nil
	}
	if o.Form == FormMaterialized {
		return WithDetail(ErrQueryEnvelopeAxis, "output.form materialized")
	}
	if o.Encoding == ResultJSON {
		return WithDetail(ErrQueryEnvelopeAxis, "output.encoding json")
	}
	return nil
}

// oneOf reports whether got is among allowed, naming the field when it is not.
func oneOf(field, got string, allowed ...string) error {
	for _, want := range allowed {
		if got == want {
			return nil
		}
	}
	return WithDetail(ErrQueryEnum, field+"="+strconv.Quote(got))
}
