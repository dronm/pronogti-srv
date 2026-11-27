package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dronm/pronogti/internal/deployupdate"
	"github.com/gorilla/websocket"
)

func TestIntegrationDeploymentMetadataAndUpdates(t *testing.T) {
	db, conn := adminIntegrationDatabase(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := conn.Exec(ctx, `CREATE TABLE public.schema_migrations (version bigint, dirty boolean);
		INSERT INTO public.schema_migrations VALUES (27, true);
		INSERT INTO public.deploy_updates (release_id, admin, kiosk, updated_at) VALUES
		('20261127T100000Z-base', true, true, '2026-11-27T10:00:00Z'),
		('20261127T101000Z-admin', true, false, NULL),
		('20261127T102000Z-kiosk', false, true, NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	info, updatedAt, err := readAboutDatabase(ctx, db)
	if err != nil || info.Migration != "27 (dirty)" || !strings.HasPrefix(info.ServerVersion, "PostgreSQL ") || updatedAt.UTC().Format(time.RFC3339) != "2026-11-27T10:00:00Z" {
		t.Fatalf("read About information: %v, %v, %v", info, updatedAt, err)
	}

	socket := deployupdate.NewSocket(ctx, db, nil)
	server := httptest.NewServer(socket)
	defer server.Close()
	connect := func(app string) *websocket.Conn {
		t.Helper()
		query := url.Values{"app": {app}, "release": {"20261127T100000Z-base"}}
		address := "ws" + strings.TrimPrefix(server.URL, "http") + "/deployments/ws?" + query.Encode()
		client, _, err := websocket.DefaultDialer.DialContext(ctx, address, http.Header{"Origin": {server.URL}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	admin := connect("admin")
	kiosk := connect("kiosk")
	complete := func(release, timestamp string) {
		t.Helper()
		if _, err := conn.Exec(ctx, `UPDATE public.deploy_updates SET updated_at = $2::text::timestamptz WHERE release_id = $1`, release, timestamp); err != nil {
			t.Fatal(err)
		}
		if err := socket.PublishEvent("", deployupdate.Event, nil); err != nil {
			t.Fatal(err)
		}
	}
	requireEvent := func(client *websocket.Conn, release string) {
		t.Helper()
		var response struct {
			EventID string            `json:"event_id"`
			Payload map[string]string `json:"payload"`
		}
		_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
		if err := client.ReadJSON(&response); err != nil || response.EventID != deployupdate.Event || response.Payload["release_id"] != release {
			t.Fatalf("expected %s, received %+v (%v)", release, response, err)
		}
	}
	complete("20261127T101000Z-admin", "2026-11-27T10:10:00Z")
	requireEvent(admin, "20261127T101000Z-admin")
	complete("20261127T102000Z-kiosk", "2026-11-27T10:20:00Z")
	requireEvent(kiosk, "20261127T102000Z-kiosk")
	requireEvent(connect("kiosk"), "20261127T102000Z-kiosk")
	_, updatedAt, err = readAboutDatabase(ctx, db)
	if err != nil || updatedAt.UTC().Format(time.RFC3339) != "2026-11-27T10:20:00Z" {
		t.Fatalf("latest completed deployment = %v (%v)", updatedAt, err)
	}
}
