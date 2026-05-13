package v2

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wesm/msgvault/internal/store"
)

// TestMount_RoutesReachable sanity-checks the Register seam: every
// route the production wiring mounts is reachable via the same chi
// pattern used in server.go, version-header middleware stamps each
// response, and path-param routes resolve correctly through the
// nested mount. Catches slash/redirect/double-match oddities you can
// easily get from the chi.Mount("/", ...) pattern.
func TestMount_RoutesReachable(t *testing.T) {
	ms := &mockStore{
		messages: []store.APIMessage{{ID: 1, Subject: "Hi"}},
		total:    1,
	}
	ms.messagesV2 = map[int64]*store.APIMessageV2{99: {ID: 99, Subject: "found"}}
	ms.threads = map[int64]*store.APIThread{77: {ID: 77, Subject: "t"}}
	r := newTestRouter(t, ms)

	cases := []struct {
		name string
		path string
	}{
		{"no-param list", "/api/v2/messages"},
		{"path-param detail", "/api/v2/messages/99"},
		{"nested path-param", "/api/v2/threads/77"},
		{"path-with-query", "/api/v2/search?q=anything"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", c.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code == http.StatusNotFound {
				t.Fatalf("%s reached chi 404; route not registered or mount swallowed it", c.path)
			}
			if got := w.Header().Get("X-MsgVault-API"); got != "v2" {
				t.Errorf("%s: X-MsgVault-API = %q, want v2 (middleware not applied through mount)", c.path, got)
			}
		})
	}
}
