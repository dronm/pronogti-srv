package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/webapp"
)

type aboutDatabase struct {
	ServerType    string `json:"server_type"`
	ServerVersion string `json:"server_version"`
	Migration     string `json:"migration"`
}

func (h *adminHandler) about(w http.ResponseWriter, r *http.Request) {
	if !h.requireRead(w, r) {
		return
	}
	var database *aboutDatabase
	var updatedAt time.Time
	var ip string
	if h.options.AboutDB != nil {
		var err error
		database, updatedAt, err = readAboutDatabase(r.Context(), h.options.AboutDB)
		if err != nil {
			slog.Error("read program information", "error", err)
			webapp.WriteServiceError(w, webapp.Internal("program information is unavailable", nil))
			return
		}
		ip = backendPublicIP()
	}
	if updatedAt.IsZero() {
		updatedAt, _ = time.Parse(time.RFC3339, h.options.BuildDate)
	}
	_ = webapp.WriteJSON(w, http.StatusOK, map[string]any{
		"name": "Pronogti", "author": map[string]string{"name": "Andrey Mikhalevich", "email": "katrenplus@mail.ru"},
		"db": database, "updated_at": updatedAt.UTC(),
		"backend": map[string]string{"version": h.options.Version, "build_date": h.options.BuildDate, "commit": h.options.Commit, "ip": ip},
	})
}

func readAboutDatabase(ctx context.Context, db ds.Provider) (*aboutDatabase, time.Time, error) {
	lease, server, err := db.GetPrimary(ctx)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("get primary database connection: %w", err)
	}
	defer db.Release(lease, server)
	conn := lease.Conn()
	database := &aboutDatabase{ServerType: "PostgreSQL", Migration: "unknown"}
	var updatedAt sql.NullTime
	var hasMigrations bool
	err = conn.QueryRow(ctx, `SELECT version(), (SELECT max(updated_at) FROM public.deploy_updates),
		to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&database.ServerVersion, &updatedAt, &hasMigrations)
	if err != nil {
		return nil, time.Time{}, err
	}
	if hasMigrations {
		err = conn.QueryRow(ctx, `SELECT COALESCE((SELECT version::text || CASE WHEN dirty THEN ' (dirty)' ELSE '' END
			FROM public.schema_migrations LIMIT 1), 'unknown')`).Scan(&database.Migration)
	}
	return database, updatedAt.Time, err
}

// Use the same external address lookup as Meatshop and cache it per process.
var backendPublicIP = sync.OnceValue(func() string {
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("https://ifconfig.me/ip")
	if err != nil {
		slog.Warn("look up public backend address", "error", err)
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 128))
	if err != nil {
		return ""
	}
	ip := net.ParseIP(strings.TrimSpace(string(body)))
	if ip == nil {
		return ""
	}
	return ip.String()
})
