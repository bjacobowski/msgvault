package store_test

import (
	"testing"

	"github.com/wesm/msgvault/internal/store"
)

func TestLiveMessagesWhere(t *testing.T) {
	cases := []struct {
		alias                 string
		hideDeletedFromSource bool
		want                  string
	}{
		{"", true, "deleted_from_source_at IS NULL"},
		{"", false, "1=1"},
		{"m", true, "m.deleted_from_source_at IS NULL"},
		{"m", false, "1=1"},
		{"msg", true, "msg.deleted_from_source_at IS NULL"},
		{"msg", false, "1=1"},
	}
	for _, tc := range cases {
		got := store.LiveMessagesWhere(tc.alias, tc.hideDeletedFromSource)
		if got != tc.want {
			t.Errorf("LiveMessagesWhere(%q, %v) = %q, want %q",
				tc.alias, tc.hideDeletedFromSource, got, tc.want)
		}
	}
}
