package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/session"
	"github.com/dronm/webapp"
)

type AdminService interface {
	Login(context.Context, session.Session, adminauth.Credentials, adminauth.RequestInfo) (adminauth.Response, error)
	Current(context.Context, session.Session) (adminauth.Response, error)
	Logout(context.Context, session.Session, string) error
	ChangePassword(context.Context, session.Session, string, adminauth.PasswordChange) error
}

type AdminHTTPOptions struct {
	AllowedOrigins             []string
	RequestTimeout             time.Duration
	Version, Commit, BuildDate string
	Navigation                 AdminNavigation
	Refunds                    AdminRefunds
	GeneratedRoutes            http.Handler
	GeneratedPermissions       []string
}

type adminHandler struct {
	service  AdminService
	sessions *AdminSessions
	options  AdminHTTPOptions
}

// NewAdminHandler has its own session/origin stack. It is never wrapped in the
// public salon or persistent kiosk resolver.
func NewAdminHandler(service AdminService, sessions *AdminSessions, options AdminHTTPOptions) http.Handler {
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 30 * time.Second
	}
	h := &adminHandler{service: service, sessions: sessions, options: options}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/login", h.login)
	mux.HandleFunc("GET /admin/session", h.current)
	mux.HandleFunc("POST /admin/logout", h.logout)
	mux.HandleFunc("POST /admin/profile/password", h.changePassword)
	mux.HandleFunc("GET /admin/main-menu", h.mainMenu)
	mux.HandleFunc("GET /admin/permissions", h.permissions)
	mux.HandleFunc("GET /admin/prog-about", h.about)
	h.registerNavigation(mux)
	h.registerRefunds(mux)
	if options.GeneratedRoutes != nil {
		mux.Handle("/", options.GeneratedRoutes)
	}
	var resolver webapp.SessionResolver
	if sessions != nil {
		resolver = sessions.Resolver
	}
	cors := webapp.CORSConfig{AllowedOrigins: options.AllowedOrigins, AllowedMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}, AllowedHeaders: []string{"Content-Type", adminauth.SessionHeader, webapp.DefaultQueryIDHeader, refundIdempotencyHeader}, ExposedHeaders: []string{webapp.DefaultQueryIDHeader, "Location"}, AllowCredentials: true, MaxAgeSeconds: 600}
	return webapp.Chain(webapp.RecoverMiddleware(), webapp.QueryIDMiddleware(""), webapp.RequestLoggerMiddleware(), webapp.CORSMiddleware(cors), adminOriginMiddleware(options.AllowedOrigins), newAdminLoginLimiter().middleware, webapp.SessionMiddleware(resolver), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			ctx, cancel := context.WithTimeout(r.Context(), options.RequestTimeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})(mux)
}

// WithAdministrator mounts the complete namespace, including unknown paths, so
// no administrative request can fall through to the customer middleware stack.
func WithAdministrator(public, admin http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/") {
			if admin == nil {
				http.NotFound(w, r)
				return
			}
			admin.ServeHTTP(w, r)
			return
		}
		public.ServeHTTP(w, r)
	})
}

func (h *adminHandler) login(w http.ResponseWriter, r *http.Request) {
	var credentials adminauth.Credentials
	if err := decodeAdminJSON(w, r, &credentials); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	if h.service == nil {
		webapp.WriteServiceError(w, errors.New("administrator authentication is not configured"))
		return
	}
	response, err := h.service.Login(r.Context(), webapp.SessionFromContext(r.Context()), credentials, adminauth.RequestInfo{IP: adminRemoteIP(r), UserAgent: r.UserAgent()})
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, http.StatusOK, response)
}

func (h *adminHandler) current(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		webapp.WriteServiceError(w, errors.New("administrator authentication is not configured"))
		return
	}
	response, err := h.service.Current(r.Context(), webapp.SessionFromContext(r.Context()))
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, http.StatusOK, response)
}

func (h *adminHandler) logout(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if err := decodeAdminJSON(w, r, &body); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	if h.service == nil || h.sessions == nil {
		webapp.WriteServiceError(w, errors.New("administrator authentication is not configured"))
		return
	}
	sess := webapp.SessionFromContext(r.Context())
	if err := h.service.Logout(r.Context(), sess, r.Header.Get(adminauth.SessionHeader)); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	if err := h.sessions.End(w, r, sess); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, http.StatusOK, adminauth.Response{})
}

func (h *adminHandler) changePassword(w http.ResponseWriter, r *http.Request) {
	var change adminauth.PasswordChange
	if err := decodeAdminJSON(w, r, &change); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	if h.service == nil || h.sessions == nil {
		webapp.WriteServiceError(w, errors.New("administrator authentication is not configured"))
		return
	}
	sess := webapp.SessionFromContext(r.Context())
	if err := h.service.ChangePassword(r.Context(), sess, r.Header.Get(adminauth.SessionHeader), change); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	if err := h.sessions.End(w, r, sess); err != nil {
		slog.WarnContext(r.Context(), "could not clean up administrator session after password change")
	}
	_ = webapp.WriteJSON(w, http.StatusOK, adminauth.Response{})
}

func (h *adminHandler) requireRead(w http.ResponseWriter, r *http.Request) bool {
	if h.service == nil {
		webapp.WriteServiceError(w, errors.New("administrator authentication is not configured"))
		return false
	}
	response, err := h.service.Current(r.Context(), webapp.SessionFromContext(r.Context()))
	if err != nil {
		webapp.WriteServiceError(w, err)
		return false
	}
	if response.User == nil || response.User.RoleID != "admin" || response.Auth == nil {
		webapp.WriteServiceError(w, webapp.Unauthorized("administrator session required", map[string]any{"code": "admin_session_required"}))
		return false
	}
	return true
}

func (h *adminHandler) mainMenu(w http.ResponseWriter, r *http.Request) {
	h.navigationForUser(w, r)
}

func (h *adminHandler) permissions(w http.ResponseWriter, r *http.Request) {
	if !h.requireRead(w, r) {
		return
	}
	permissions := append([]string{"admin.access"}, h.options.GeneratedPermissions...)
	_ = webapp.WriteJSON(w, http.StatusOK, permissions)
}

func (h *adminHandler) about(w http.ResponseWriter, r *http.Request) {
	if !h.requireRead(w, r) {
		return
	}
	_ = webapp.WriteJSON(w, http.StatusOK, map[string]any{"name": "Pronogti", "author": map[string]string{"name": "", "email": ""}, "db": nil, "updated_at": time.Now().UTC(), "backend": map[string]string{"version": h.options.Version, "build_date": h.options.BuildDate, "commit": h.options.Commit, "ip": ""}})
}

func decodeAdminJSON(w http.ResponseWriter, r *http.Request, value any) error {
	return decodeAdminJSONLimit(w, r, value, 4096)
}

func decodeAdminJSONLimit(w http.ResponseWriter, r *http.Request, value any, limit int64) error {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return webapp.BadRequest("administrator requests require application/json", nil)
	}
	if r.Body == nil {
		return webapp.BadRequest("JSON request body is required", nil)
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return webapp.BadRequest("invalid administrator request", nil)
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return webapp.BadRequest("administrator request must be a JSON object", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		return webapp.BadRequest("invalid administrator request", nil)
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return webapp.BadRequest("administrator request must contain one JSON object", nil)
	}
	return nil
}

func adminRemoteIP(r *http.Request) string {
	address := r.RemoteAddr
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	if ip := net.ParseIP(address); ip != nil {
		return ip.String()
	}
	return ""
}

func adminOriginMiddleware(origins []string) webapp.Middleware {
	allowed := make(map[string]bool, len(origins))
	for _, origin := range origins {
		allowed[strings.TrimRight(origin, "/")] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && !allowed[origin] || origin == "" && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				webapp.WriteServiceError(w, webapp.Forbidden("administrator request origin is not allowed", nil))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type adminLoginWindow struct {
	started time.Time
	count   int
}
type adminLoginLimiter struct {
	mu      sync.Mutex
	entries map[string]adminLoginWindow
	now     func() time.Time
}

func newAdminLoginLimiter() *adminLoginLimiter {
	return &adminLoginLimiter{entries: make(map[string]adminLoginWindow), now: time.Now}
}
func (l *adminLoginLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/admin/login" {
			next.ServeHTTP(w, r)
			return
		}
		now, key := l.now(), adminRemoteIP(r)
		l.mu.Lock()
		if len(l.entries) >= 4096 {
			for id, entry := range l.entries {
				if now.Sub(entry.started) >= time.Minute {
					delete(l.entries, id)
				}
			}
		}
		entry, exists := l.entries[key]
		if !exists && len(l.entries) >= 4096 {
			l.mu.Unlock()
			w.Header().Set("Retry-After", "60")
			webapp.WriteServiceError(w, webapp.NewHTTPError(429, "too_many_requests", "administrator login temporarily rate limited", nil))
			return
		}
		if now.Sub(entry.started) >= time.Minute {
			entry = adminLoginWindow{started: now}
		}
		if entry.started.IsZero() {
			entry.started = now
		}
		entry.count++
		l.entries[key] = entry
		l.mu.Unlock()
		if entry.count > 15 {
			w.Header().Set("Retry-After", "60")
			webapp.WriteServiceError(w, webapp.NewHTTPError(429, "too_many_requests", "administrator login temporarily rate limited", nil))
			return
		}
		next.ServeHTTP(w, r)
	})
}
