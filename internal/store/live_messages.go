package store

import "fmt"

// liveMessages* cover the two (alias) combinations on hot read paths
// for hideDeletedFromSource=true; pre-computing them avoids a
// fmt.Sprintf allocation per call on list/search/count paths.
const (
	liveMessagesUnaliasedFull = "deleted_from_source_at IS NULL"
	liveMessagesMFull         = "m.deleted_from_source_at IS NULL"
)

// LiveMessagesWhere returns the SQL predicate that selects live
// messages. Source-deleted rows (deleted_from_source_at IS NOT NULL)
// are filtered only when hideDeletedFromSource is true; archive views
// may intentionally show source-deleted rows.
//
// Pass the table alias used in the surrounding query (use "" if the
// query has no alias). When hideDeletedFromSource is false the
// predicate is the always-true "1=1" so callers can keep their
// "WHERE %s AND …" template unconditionally.
//
// The two common alias values ("" and "m") with hideDeletedFromSource
// true are returned from package-level constants to keep this
// allocation-free on hot paths. Other aliases fall back to fmt.Sprintf.
func LiveMessagesWhere(alias string, hideDeletedFromSource bool) string {
	if !hideDeletedFromSource {
		return "1=1"
	}
	switch alias {
	case "":
		return liveMessagesUnaliasedFull
	case "m":
		return liveMessagesMFull
	}
	return fmt.Sprintf("%s.deleted_from_source_at IS NULL", alias)
}
