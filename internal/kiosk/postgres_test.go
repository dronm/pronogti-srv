package kiosk

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/dronm/ds/v4"
)

type registryTestProvider struct {
	ds.Provider
	conn     *registryTestConn
	released bool
}

func (p *registryTestProvider) GetPrimary(context.Context) (ds.PoolConn, ds.ServerID, error) {
	return registryTestLease{conn: p.conn}, "primary", nil
}
func (p *registryTestProvider) Release(ds.PoolConn, ds.ServerID) { p.released = true }

type registryTestLease struct{ conn ds.Conn }

func (l registryTestLease) Conn() ds.Conn { return l.conn }
func (l registryTestLease) Release()      {}

type registryTestConn struct {
	ds.Conn
	sql  string
	args []any
	err  error
}

func (c *registryTestConn) QueryRow(_ context.Context, sql string, args ...any) ds.Row {
	c.sql, c.args = sql, args
	return registryTestRow{err: c.err}
}

type registryTestRow struct{ err error }

func (r registryTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*int64)) = 7
	*(dest[1].(*string)) = "Desk"
	*(dest[2].(*int64)) = 12
	*(dest[3].(*bool)) = true
	*(dest[4].(*int64)) = 3
	return nil
}

func TestRegistryLookupHashesCredentialAndReleasesLease(t *testing.T) {
	conn := &registryTestConn{}
	provider := &registryTestProvider{conn: conn}
	registry := NewPostgresRegistry(provider)
	item, err := registry.FindByToken(context.Background(), "secret-device-token")
	if err != nil || item.ID != 7 || item.CompanyID != 12 || item.SessionVersion != 3 {
		t.Fatalf("lookup result=%+v error=%v", item, err)
	}
	if !provider.released || len(conn.args) != 1 || conn.args[0] != HashToken("secret-device-token") || strings.Contains(conn.sql, "secret-device-token") {
		t.Fatal("credential not hashed or lease not released")
	}
	conn.err = ds.ErrNoRows
	if _, err := registry.FindByID(context.Background(), 999); !errors.Is(err, ErrKioskNotFound) {
		t.Fatalf("missing kiosk error=%v", err)
	}
}

func TestRegistryProvisioningPersistsOnlyDigest(t *testing.T) {
	conn := &registryTestConn{}
	registry := NewPostgresRegistry(&registryTestProvider{conn: conn})
	_, token, err := registry.Create(context.Background(), " Desk ", 12)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := hex.DecodeString(token)
	if err != nil || len(bytes) != 32 {
		t.Fatalf("credential has wrong entropy/format")
	}
	if len(conn.args) != 3 || conn.args[0] != "Desk" || conn.args[2] != HashToken(token) || conn.args[2] == token {
		t.Fatal("raw credential was persisted")
	}
	rotated, err := registry.Rotate(context.Background(), 7)
	if err != nil || rotated == token || conn.args[1] != HashToken(rotated) {
		t.Fatalf("rotate failed: %v", err)
	}
	if err := registry.SetActive(context.Background(), 7, false); err != nil || conn.args[1] != false {
		t.Fatalf("disable failed: %v", err)
	}
}

func TestRegistryInvalidProvisioningDoesNotQuery(t *testing.T) {
	conn := &registryTestConn{}
	registry := NewPostgresRegistry(&registryTestProvider{conn: conn})
	if _, _, err := registry.Create(context.Background(), "", 12); err == nil {
		t.Fatal("accepted empty name")
	}
	if _, _, err := registry.Create(context.Background(), "Desk", 0); err == nil {
		t.Fatal("accepted invalid company")
	}
	if _, err := registry.Rotate(context.Background(), 0); err == nil {
		t.Fatal("accepted invalid kiosk")
	}
	if conn.sql != "" {
		t.Fatal("invalid request reached the database")
	}
}
