package v2

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wesm/msgvault/internal/search"
	"github.com/wesm/msgvault/internal/store"
)

// Store is the narrow set of store operations the v2 handlers need.
// Method names mirror the underlying store package (V2 stays in the
// method name because that's the store-layer schema identifier); the
// v2 *types* drop the suffix because version lives in the package
// path.
type Store interface {
	GetMessageV2(id int64) (*store.APIMessageV2, error)
	GetMessageV2ByRFC822ID(rfc822ID string) (*store.APIMessageV2, error)
	ListMessages(offset, limit int) ([]store.APIMessage, int64, error)
	ListMessagesByLabel(name string, offset, limit int) ([]store.APIMessage, int64, error)
	ListMessagesByParticipant(id int64, offset, limit int) ([]store.APIMessage, int64, error)
	GetThread(id int64) (*store.APIThread, error)
	GetMessagesSummariesByIDs(ids []int64) ([]store.APIMessage, error)
	SearchMessages(query string, offset, limit int) ([]store.APIMessage, int64, error)
	SearchMessagesQuery(q *search.Query, offset, limit int) ([]store.APIMessage, int64, error)
	BatchStructuredRecipients(ids []int64) (map[int64]*store.APIRecipientsV2, error)
	BatchMessageMetaV2(ids []int64) (map[int64]*store.APIMessageMetaV2, error)
	GetAttachmentByIDV2(id int64) (*store.APIAttachmentDetailV2, error)
	GetParticipantByIDV2(id int64) (*store.APIParticipantV2, error)
}

// Handler holds the dependencies the v2 routes need. The parent api
// package constructs a Handler and registers its routes under /api/v2.
type Handler struct {
	Store  Store
	Logger *slog.Logger
}

// NewHandler builds a Handler. Either field may be nil — handlers
// guard for a nil Store and return 503, matching v1's posture.
func NewHandler(s Store, logger *slog.Logger) *Handler {
	return &Handler{Store: s, Logger: logger}
}

// Register attaches every v2 route to the given chi router. The router
// is expected to already be scoped to /api/v2 and to have any
// version-header / auth middleware applied by the caller; Register
// adds only handlers, not middleware.
//
// One v2 route deliberately stays out of this set:
// /api/v2/messages/{id}/body. That endpoint returns raw bytes with the
// right Content-Type and reuses the v1 handler verbatim — there is no
// JSON shape to break. The parent api package mounts it directly so
// v2 doesn't need to know about raw-body plumbing.
func (h *Handler) Register(r chi.Router) {
	r.Get("/messages", h.handleListMessages)
	r.Get("/messages/{id}", h.handleGetMessage)
	r.Get("/messages/by-rfc822-id/{rfc822_id}", h.handleGetMessageByRFC822ID)
	r.Get("/threads/{id}", h.handleGetThread)
	r.Get("/labels/{name}/messages", h.handleListMessagesByLabel)
	r.Get("/participants/{id}", h.handleGetParticipant)
	r.Get("/participants/{id}/messages", h.handleListMessagesByParticipant)
	r.Get("/attachments/{id}", h.handleGetAttachment)
	r.Get("/search", h.handleSearch)
}

// APIVersionHeader stamps every response under /api/v2 with the v2
// contract marker so consumers (e.g. radical-roc artifacts) can detect
// drift. Replaces the global v1 header for v2-mounted routes.
func APIVersionHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-MsgVault-API", "v2")
		next.ServeHTTP(w, r)
	})
}
