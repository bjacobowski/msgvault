// Package api provides the HTTP API server for msgvault.
package api

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/wesm/msgvault/internal/config"
	"github.com/wesm/msgvault/internal/query"
	"github.com/wesm/msgvault/internal/scheduler"
	"github.com/wesm/msgvault/internal/search"
	"github.com/wesm/msgvault/internal/store"
	"github.com/wesm/msgvault/internal/vector"
	"github.com/wesm/msgvault/internal/vector/hybrid"
)

// MessageStore defines the store operations the API needs.
type MessageStore interface {
	GetStats() (*StoreStats, error)
	ListMessages(offset, limit int) ([]APIMessage, int64, error)
	GetMessage(id int64) (*APIMessage, error)
	GetMessageByRFC822ID(rfc822ID string) (*APIMessage, error)
	GetMessageBodies(id int64) (text, html string, err error)
	GetMessagesSummariesByIDs(ids []int64) ([]APIMessage, error)
	SearchMessages(query string, offset, limit int) ([]APIMessage, int64, error)
	SearchMessagesQuery(q *search.Query, offset, limit int) ([]APIMessage, int64, error)
}

// StoreStats is an alias for store.Stats — single source of truth.
type StoreStats = store.Stats

// SyncScheduler defines the scheduler operations the API needs.
type SyncScheduler interface {
	IsScheduled(email string) bool
	TriggerSync(email string) error
	AddAccount(email, schedule string) error
	Status() []AccountStatus
	IsRunning() bool
}

// AccountStatus is an alias for scheduler.AccountStatus — single source of truth.
type AccountStatus = scheduler.AccountStatus

// Server represents the HTTP API server.
type Server struct {
	cfg            *config.Config
	store          MessageStore
	engine         query.Engine // Query engine for aggregates and TUI support
	hybridEngine   *hybrid.Engine
	vectorCfg      vector.Config
	backend        vector.Backend
	scheduler      SyncScheduler
	logger         *slog.Logger
	requestTimeout time.Duration
	router         chi.Router
	server         *http.Server
	rateLimiter    *RateLimiter
	cfgMu          sync.RWMutex // protects cfg.Accounts
}

// ServerOptions configures the API server.
type ServerOptions struct {
	Config       *config.Config
	Store        MessageStore
	Engine       query.Engine // Optional: query engine for aggregates and TUI support
	HybridEngine *hybrid.Engine
	VectorCfg    vector.Config
	Backend      vector.Backend
	Scheduler    SyncScheduler
	Logger       *slog.Logger
	// RequestTimeout caps each request via chi's gentle Timeout
	// middleware. Zero defaults to 60s. The underlying http.Server's
	// WriteTimeout is set to RequestTimeout + 5s so the chi timeout
	// always fires first, preserving the structured error response.
	// Tests use a much shorter value to exercise the chi-timeout-fires
	// path.
	RequestTimeout time.Duration
}

// NewServer creates a new API server.
func NewServer(cfg *config.Config, store MessageStore, sched SyncScheduler, logger *slog.Logger) *Server {
	return NewServerWithOptions(ServerOptions{
		Config:    cfg,
		Store:     store,
		Scheduler: sched,
		Logger:    logger,
	})
}

// NewServerWithOptions creates a new API server with full options including query engine.
func NewServerWithOptions(opts ServerOptions) *Server {
	timeout := opts.RequestTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	s := &Server{
		cfg:            opts.Config,
		store:          opts.Store,
		engine:         opts.Engine,
		hybridEngine:   opts.HybridEngine,
		vectorCfg:      opts.VectorCfg,
		backend:        opts.Backend,
		scheduler:      opts.Scheduler,
		logger:         opts.Logger,
		requestTimeout: timeout,
	}
	s.router = s.setupRouter()
	return s
}

// setupRouter configures the chi router with all routes and middleware.
func (s *Server) setupRouter() chi.Router {
	r := chi.NewRouter()

	// Standard middleware
	r.Use(chimw.RequestID)
	r.Use(s.loggerMiddleware)
	r.Use(chimw.Recoverer)
	// chi/v5's Timeout is "gentle": it wraps the request context with a
	// deadline and, after next.ServeHTTP returns, conditionally writes
	// a 504. Because handlers are inline, our structured 503 (e.g.
	// embedding_timeout) is written first; chi's deferred WriteHeader
	// is then a no-op against the already-written response.
	// TestHandleSearch_HybridEmbeddingTimeoutFiresChi locks this
	// contract — if a future chi version switches to a preemptive
	// timeout (http.TimeoutHandler-style), that test will fail.
	r.Use(chimw.Timeout(s.requestTimeout))

	// API contract version header (radical-roc and similar consumers can
	// pin against this).
	r.Use(APIVersionHeaderMiddleware)

	// CORS middleware (config-driven; disabled when no origins configured).
	// When public_read is on and no origins were configured, default to "*"
	// so file:// artifacts (Origin: null) can fetch read endpoints. The
	// blast radius is bounded because public_read only relaxes GET/HEAD
	// auth, and the server still defaults to loopback bind.
	corsConfig := CORSConfig{
		AllowedOrigins:   s.cfg.Server.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-API-Key"},
		AllowCredentials: s.cfg.Server.CORSCredentials,
		MaxAge:           s.cfg.Server.CORSMaxAge,
	}
	if s.cfg.Server.PublicRead && len(corsConfig.AllowedOrigins) == 0 {
		corsConfig.AllowedOrigins = []string{"*"}
	}
	if corsConfig.MaxAge == 0 && len(corsConfig.AllowedOrigins) > 0 {
		corsConfig.MaxAge = 86400
	}
	r.Use(CORSMiddleware(corsConfig))

	// Rate limiting (10 req/sec with burst of 20)
	s.rateLimiter = NewRateLimiter(10, 20)
	r.Use(RateLimitMiddleware(s.rateLimiter))

	// Health check (no auth required)
	r.Get("/health", s.handleHealth)
	r.Head("/health", s.handleHealth)

	// API routes. The /api/v1 mount splits into two groups:
	//
	//   * Read group — GET/HEAD endpoints that are safe to expose without
	//     an API key when [server].public_read is true. Used by file://
	//     review-app artifacts (e.g. radical-roc) that can't carry a
	//     credential without leaking it.
	//   * Write group — POSTs and anything privileged. Always requires the
	//     API key when one is configured.
	r.Route("/api/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(s.publicReadOrAuth)

			r.Get("/stats", s.handleStats)
			r.Get("/messages", s.handleListMessages)
			r.Get("/messages/{id}", s.handleGetMessage)
			r.Get("/messages/{id}/body", s.handleMessageBody)
			r.Get("/messages/{id}/inline", s.handleMessageInline)
			r.Get("/messages/by-rfc822-id/{rfc822_id}", s.handleGetMessageByRFC822ID)
			r.Get("/search", s.handleSearch)
			r.Get("/aggregates", s.handleAggregates)
			r.Get("/aggregates/sub", s.handleSubAggregates)
			r.Get("/messages/filter", s.handleFilteredMessages)
			r.Get("/stats/total", s.handleTotalStats)
			r.Get("/search/fast", s.handleFastSearch)
			r.Get("/search/deep", s.handleDeepSearch)
			r.Get("/accounts", s.handleListAccounts)
			r.Get("/scheduler/status", s.handleSchedulerStatus)
		})

		r.Group(func(r chi.Router) {
			r.Use(s.authMiddleware)

			r.Post("/query", s.handleQuery)
			r.Post("/accounts", s.handleAddAccount)
			r.Post("/sync/{account}", s.handleTriggerSync)
			r.Post("/auth/token/{email}", s.handleUploadToken)
		})
	})

	return r
}

// Start begins listening for HTTP requests.
// Returns an error if the security posture is invalid.
func (s *Server) Start() error {
	if err := s.cfg.Server.ValidateSecure(); err != nil {
		return err
	}

	bindAddr := s.cfg.Server.BindAddr
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}
	addr := net.JoinHostPort(bindAddr, strconv.Itoa(s.cfg.Server.APIPort))

	if s.cfg.Server.APIKey == "" {
		s.logger.Warn("API server running without authentication — set [server] api_key in config.toml")
	}

	// WriteTimeout must comfortably exceed the chi request timeout so
	// the inner timeout always fires first; otherwise a request whose
	// chi deadline equals the server WriteTimeout could lose the race
	// and have its TCP connection torn down before the structured
	// error response reaches the client. The 5s buffer covers chi's
	// deferred WriteHeader plus response flush overhead.
	writeTimeout := s.requestTimeout + 5*time.Second
	s.server = &http.Server{
		Addr:         addr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: writeTimeout,
		IdleTimeout:  120 * time.Second,
	}

	s.logger.Info("starting API server", "addr", addr)
	return s.server.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.rateLimiter != nil {
		s.rateLimiter.Close()
	}
	if s.server == nil {
		return nil
	}
	s.logger.Info("shutting down API server")
	return s.server.Shutdown(ctx)
}

// Router returns the chi router for testing.
func (s *Server) Router() chi.Router {
	return s.router
}

// loggerMiddleware logs HTTP requests.
func (s *Server) loggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

		defer func() {
			s.logger.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration", time.Since(start),
				"request_id", chimw.GetReqID(r.Context()),
			)
		}()

		next.ServeHTTP(ww, r)
	})
}

// publicReadOrAuth permits unauthenticated GET/HEAD when
// [server].public_read is true; otherwise it falls through to the standard
// API-key check. Used on the read endpoints under /api/v1 so review apps
// served from file:// can fetch citations without embedding a key.
func (s *Server) publicReadOrAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Server.PublicRead && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			next.ServeHTTP(w, r)
			return
		}
		s.authMiddleware(next).ServeHTTP(w, r)
	})
}

// authMiddleware validates the API key.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth if no API key configured
		if s.cfg.Server.APIKey == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Check Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			// Also check X-API-Key header
			authHeader = r.Header.Get("X-API-Key")
		}

		// Strip "Bearer " prefix if present
		if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
			authHeader = authHeader[7:]
		}

		if subtle.ConstantTimeCompare([]byte(authHeader), []byte(s.cfg.Server.APIKey)) != 1 {
			s.logger.Warn("unauthorized API request",
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
			)
			writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or missing API key")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// handleHealth returns a simple health check response.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
