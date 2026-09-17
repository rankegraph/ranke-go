// package: tests/rql / integration
// type:    tool
// job:     execute a corpus query against any Universe and render its answer as a deterministic
// fingerprint — the comparable form the conformance matrix diffs across backends
// limits:  no assertions and no backend knowledge; the corpus lives alongside (-> corpus.go) and
// the comparing is the matrix's job (-> tests/matrix)
package rql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/rankegraph/ranke-go"
)

// Answer is one backend's response to one query: a fingerprint per result, in the
// order the stream emitted them, compared as a sequence since order is contract.
type Answer []string

// Run executes q through the Archive at archiveHead, so the query meets the
// library's own scope resolution rather than a hand-rolled Scope.
//
// Every answer passes VerifyAnswer on the way out, so each rule there is checked on
// every corpus query and every row without a test asking for it. A violation is the
// error, since an answer breaking a rule is not an answer.
func Run(ctx context.Context, u ranke.Universe, q ranke.Query, archiveHead ranke.Id) (Answer, error) {
	arc, err := ranke.NewArchive(ctx, u, archiveHead)
	if err != nil {
		return nil, err
	}
	rs, err := arc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	var elements []ranke.QueryResult
	for rs.Next() {
		elements = append(elements, rs.Result())
	}
	if err := rs.Err(); err != nil {
		_ = rs.Close()
		return nil, err
	}
	if err := rs.Close(); err != nil {
		return nil, err
	}
	if vs := VerifyAnswer(ctx, arc, u, q, elements, skipEnvRules()...); len(vs) > 0 {
		return nil, violationsError(q, vs)
	}
	out := make(Answer, 0, len(elements))
	for _, el := range elements {
		out = append(out, Fingerprint(el))
	}
	return out, nil
}

// Fingerprint renders one result deterministically — id, kind, path, claim fields
// and edges, encoded bytes — so every output axis is compared, not just the ids.
func Fingerprint(r ranke.QueryResult) string {
	var b strings.Builder
	// The id leads, so a caller can read it straight off the fingerprint.
	b.WriteString(idOf(r.ClaimId))
	b.WriteString(" kind=")
	b.WriteString(string(r.Kind))

	if len(r.PathId) > 0 {
		b.WriteString(" path[")
		for i, id := range r.PathId {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(idOf(id))
		}
		b.WriteByte(']')
	}

	// ClaimNative is nil under DetailID and the encoded kinds — the id or the bytes
	// are the payload, which is exactly what those ask for.
	if r.ClaimNative != nil {
		b.WriteString(" claim{")
		b.WriteString(claimFields(r.ClaimNative))
		b.WriteString(edgeList(r.ClaimNative))
		b.WriteByte('}')
	}

	// Hash the serialised bytes rather than inline them, so a mismatch stays
	// readable, and carry the length so a cutoff at the cap is visible.
	b.WriteString(digest("encoded", r.ClaimEncoded))
	for _, e := range r.PathEncoded {
		b.WriteString(digest("hop", e))
	}
	return b.String()
}

// digest renders a byte payload as its length plus a short hash.
func digest(label string, b []byte) string {
	if b == nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf(" %s{len=%d sha=%s}", label, len(b), hex.EncodeToString(sum[:6]))
}

// claimFields renders the node's projected fields — the ones RQL can filter and
// order on, so a divergence in any of them shows up here.
func claimFields(c ranke.Claim) string {
	n := c.Node()
	return fmt.Sprintf("type=%s height=%d size=%d encoding=%s created=%s",
		n.Type(), n.Height(), n.GetContentSize(), n.Encoding(),
		n.CreatedAt().UTC().Format("2006-01-02T15:04:05.999999999Z"))
}

// edgeList renders the claim's edges in the order the claim carries them. Order
// is significant: a claim's id commits to its serialized edge sequence, so a
// reconstruction that reorders them would not round-trip to the same id.
func edgeList(c ranke.Claim) string {
	edges := c.Edges()
	if len(edges) == 0 {
		return " edges[]"
	}
	var b strings.Builder
	b.WriteString(" edges[")
	for i, e := range edges {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s->%s", e.Type(), idOf(e.Reference()))
		if d := e.RelationDirection(); d != 0 {
			fmt.Fprintf(&b, "(%d)", d)
		}
	}
	b.WriteByte(']')
	return b.String()
}

// idOf renders an id, or "-" when absent, so a nil never formats as a panic or
// an empty gap in a fingerprint.
func idOf(id ranke.Id) string {
	if id == nil {
		return "-"
	}
	return id.String()
}

// Describe renders a query's RQL on one line — the compact form the timing
// report prints. Just the meaningful parts: scope, path/closure, filter, order,
// limit.
func Describe(q ranke.Query) string {
	parts := []string{"branch=" + q.Select.Branch}
	if len(q.Select.Path) == 0 {
		// The two empties read differently: an absent path is the closure, an empty
		// one the frontier it would have expanded (`R-QSTEPS`).
		if q.Select.Path == nil {
			parts = append(parts, "closure")
		} else {
			parts = append(parts, "frontier")
		}
	} else {
		for _, s := range q.Select.Path {
			seg := "path("
			if s.Dir != "" {
				seg += "dir=" + string(s.Dir) + " "
			}
			if len(s.Edges) > 0 {
				seg += "edges=" + strings.Join(s.Edges, ",") + " "
			}
			if len(s.Nodes) > 0 {
				seg += "nodes=" + strings.Join(s.Nodes, ",") + " "
			}
			if s.Max > 0 {
				seg += fmt.Sprintf("depth=%d", s.Max)
			}
			parts = append(parts, strings.TrimSpace(seg)+")")
		}
	}
	if q.Where != nil {
		parts = append(parts, "where "+describeWhere(q.Where))
	}
	for _, k := range q.Order {
		dir := "asc"
		if k.Dir == ranke.SortDesc {
			dir = "desc"
		}
		parts = append(parts, "order("+k.Field+" "+dir+")")
	}
	if q.Limit.Results > 0 {
		parts = append(parts, fmt.Sprintf("limit=%d", q.Limit.Results))
	}
	return strings.Join(parts, " ")
}

func describeWhere(w *ranke.Where) string {
	switch {
	case len(w.And) > 0:
		return "(" + joinWheres(w.And, " and ") + ")"
	case len(w.Or) > 0:
		return "(" + joinWheres(w.Or, " or ") + ")"
	case w.Not != nil:
		return "not " + describeWhere(w.Not)
	case w.Test != nil:
		return w.Field + describeCmp(w.Test)
	}
	return "?"
}

func joinWheres(ws []ranke.Where, sep string) string {
	p := make([]string, len(ws))
	for i := range ws {
		p[i] = describeWhere(&ws[i])
	}
	return strings.Join(p, sep)
}

func describeCmp(c *ranke.Comparison) string {
	switch {
	case c.Glob != "":
		return " glob " + c.Glob
	case c.Eq != nil:
		return fmt.Sprintf(" == %v", c.Eq)
	case c.Ne != nil:
		return fmt.Sprintf(" != %v", c.Ne)
	case c.Lt != nil:
		return fmt.Sprintf(" < %v", c.Lt)
	case c.Le != nil:
		return fmt.Sprintf(" <= %v", c.Le)
	case c.Gt != nil:
		return fmt.Sprintf(" > %v", c.Gt)
	case c.Ge != nil:
		return fmt.Sprintf(" >= %v", c.Ge)
	case len(c.In) > 0:
		return fmt.Sprintf(" in %v", c.In)
	}
	return "?"
}
