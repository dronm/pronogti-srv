package deployupdate

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/webapp/ws"
	"github.com/gorilla/websocket"
)

const Event = "DeployUpdate.Insert"

var releasePattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9]{1,32}$`)

type Socket struct {
	ctx      context.Context
	db       ds.Provider
	upgrader websocket.Upgrader
	mu       sync.Mutex
	updates  chan struct{}
}

func NewSocket(ctx context.Context, db ds.Provider, allowedOrigins []string) *Socket {
	origins := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origins[strings.TrimRight(origin, "/")] = true
	}
	return &Socket{
		ctx: ctx, db: db, updates: make(chan struct{}),
		upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
			raw := r.Header.Get("Origin")
			origin, err := url.Parse(raw)
			return err == nil && (origin.Scheme == "http" || origin.Scheme == "https") &&
				origin.Host != "" && (strings.EqualFold(origin.Host, r.Host) || origins[raw])
		}},
	}
}

// PublishEvent wakes the read-only stream. The database record, rather than
// the notification payload, determines which clients have a newer release.
func (s *Socket) PublishEvent(_, eventID string, _ any) error {
	if eventID == Event {
		s.mu.Lock()
		close(s.updates)
		s.updates = make(chan struct{})
		s.mu.Unlock()
	}
	return nil
}

func (s *Socket) changes() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updates
}

func (s *Socket) newerRelease(ctx context.Context, app, currentRelease string) (string, error) {
	lease, server, err := s.db.GetPrimary(ctx)
	if err != nil {
		return "", err
	}
	defer s.db.Release(lease, server)
	var next string
	err = lease.Conn().QueryRow(ctx, `SELECT newer.release_id
		FROM public.deploy_updates current_release
		JOIN public.deploy_updates newer ON newer.id > current_release.id
		WHERE current_release.release_id = $1 AND newer.updated_at IS NOT NULL
			AND CASE WHEN $2 = 'admin' THEN current_release.admin AND newer.admin
				ELSE current_release.kiosk AND newer.kiosk END
		ORDER BY newer.id DESC LIMIT 1`, currentRelease, app).Scan(&next)
	return next, err
}

func (s *Socket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	app, currentRelease := query.Get("app"), query.Get("release")
	if (app != "admin" && app != "kiosk") || !releasePattern.MatchString(currentRelease) {
		http.Error(w, "invalid deployment subscription", http.StatusBadRequest)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(4096)
	_ = conn.SetReadDeadline(time.Now().Add(65 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(65 * time.Second))
	})
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			// Drain client messages to service control frames. This socket
			// never dispatches application methods or accepts events to publish.
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	lastSent := ""
	for {
		changed := s.changes()
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		next, err := s.newerRelease(ctx, app, currentRelease)
		cancel()
		if err != nil && !errors.Is(err, ds.ErrNoRows) && s.ctx.Err() == nil {
			slog.Warn("check frontend deployment", "app", app, "error", err)
		}
		if err == nil && next != lastSent {
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err = conn.WriteJSON(ws.ServerResponse{EventID: Event, Payload: map[string]string{"release_id": next}}); err != nil {
				return
			}
			lastSent = next
		}
		select {
		case <-s.ctx.Done():
			return
		case <-closed:
			return
		case <-changed:
		case <-ticker.C:
			// Recheck durable state even if LISTEN was briefly disconnected.
			if err = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}
}
