package v2

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/wesm/msgvault/internal/store"
)

// maxPageSize is the hard upper bound for any paginated v2 endpoint.
// Matches the v1 constant so the two surfaces enforce the same cap.
const maxPageSize = 500

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, errCode, message string) {
	writeJSON(w, status, ErrorResponse{Error: errCode, Message: message})
}

// parseLimitOffset reads limit and offset query params, clamping limit
// to [1, maxPageSize] (default 50) and offset to >= 0.
func parseLimitOffset(r *http.Request) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 50
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// stringsOrEmpty returns its input or a new empty slice when nil, so
// JSON encoding emits [] for an empty labels field.
func stringsOrEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// normalizeMessageID returns a Message-ID-style string wrapped in
// angle brackets if it isn't already. Empty input stays empty.
//
// Why this exists: internal/mime strips `<>` from values it returns
// for the References header but leaves Message-ID / In-Reply-To
// bracketed. RFC 5322 spells msg-id with the angles, and the
// /by-rfc822-id lookup keys on the DB-stored value which is
// bracketed. Normalizing at the v2 boundary keeps consumers from
// having to track which fields lost their brackets.
func normalizeMessageID(s string) string {
	if s == "" {
		return s
	}
	if len(s) >= 2 && s[0] == '<' && s[len(s)-1] == '>' {
		return s
	}
	return "<" + s + ">"
}

// addresses converts the store slice into the JSON DTO slice. Empty
// input returns an empty slice (not nil) so the JSON encoder emits []
// for the canonical to/cc/bcc fields.
func addresses(in []store.APIAddress) []EmailAddress {
	out := make([]EmailAddress, 0, len(in))
	for _, a := range in {
		out = append(out, EmailAddress{Name: a.Name, Address: a.Address})
	}
	return out
}

// inlineSafeMIMETypes is the safelist of attachment MIME types that
// may be served with `Content-Disposition: inline`. Everything else is
// forced to download to keep XSS surface flat: an HTML or SVG
// attachment served inline could execute scripts from the msgvault
// origin. Duplicated from internal/api to keep v2 free of any
// back-edge into the v1 package.
var inlineSafeMIMETypes = map[string]struct{}{
	"application/pdf": {},
	"image/png":       {},
	"image/jpeg":      {},
	"image/gif":       {},
	"image/webp":      {},
	"text/plain":      {},
}

// isInlineSafeMIME reports whether the given Content-Type can be served
// with `inline` disposition. Only the type/subtype is considered; any
// parameters (e.g. `charset=...`) are stripped.
func isInlineSafeMIME(ct string) bool {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	_, ok := inlineSafeMIMETypes[base]
	return ok
}
