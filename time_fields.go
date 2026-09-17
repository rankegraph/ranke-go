// package: ranke / taxonomy
// type:    logic
// job:     `V-TIME` for the optional timestamp fields — delete_by, pubkey_valid_from,
// pubkey_expires_after — refused wherever a claim arrives, by decode and by assemble;
// created_at is a record slot decodeNode already parses, and FormatTimestamp writes the form
// limits:  no verifyRules entry, since the closure verifier decodes every claim it walks and
// would reach a rule that can never fire; an ABSENT field is no violation, only a
// present unparsable one
package ranke

import "time"

// timeFields are the optional fields `V-TIME` governs. created_at is not here: it is
// its own record slot, parsed by decodeNode.
var timeFields = []string{FieldDeleteBy, FieldPubkeyValidFrom, FieldPubkeyExpiresAfter}

// FormatTimestamp renders t in `V-TIME` form: RFC 3339, UTC, fixed-width nanoseconds
// — the one spelling a time comparison takes (`R-QTIMEOP`).
func FormatTimestamp(t time.Time) string { return t.UTC().Format(iso8601Nano) }

// firstPossibleClaim is the day the design a claim conforms to came into being: the
// foundation paper's date, which is the day ranke-graph was founded. No archive predates
// it, so no claim was added before it, and every earlier timestamp is a default in place
// of a time — year 1 where Go writes an unset time.Time, 1970 where a clock never
// started, whatever a broken counter drifts to from there.
var firstPossibleClaim = time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)

// PredatesAnyClaim reports whether t is earlier than any claim could have been added.
// `V-MONO` requires created_at to carry the time a claim was added, and a value below
// the floor carries what an unset field reads as instead.
func PredatesAnyClaim(t time.Time) bool { return t.UTC().Before(firstPossibleClaim) }

// checkTimestampFields parses every timestamp field present in fields. Absence is no
// violation — all three are optional — so only a value that is there and will not
// parse is refused.
func checkTimestampFields(fields map[string]string) error {
	for _, name := range timeFields {
		v, ok := fields[name]
		if !ok {
			continue
		}
		if _, err := parseRFC3339Nano(v); err != nil {
			return WithDetail(ErrTimestampForm, name+"="+v)
		}
	}
	return nil
}
