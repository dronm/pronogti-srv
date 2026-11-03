package kiosk

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/dronm/ds/v4"
)

// PostgresRegistry stores device bindings independently from customer sessions.
type PostgresRegistry struct{ db ds.Provider }

func NewPostgresRegistry(db ds.Provider) *PostgresRegistry { return &PostgresRegistry{db: db} }

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func GenerateToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate kiosk credential: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func (r *PostgresRegistry) FindByToken(ctx context.Context, token string) (Kiosk, error) {
	if token == "" || len(token) > 512 {
		return Kiosk{}, ErrKioskNotFound
	}
	return r.find(ctx, `SELECT id, name, company_id, is_active, session_version FROM public.kiosks WHERE token_hash=$1`, HashToken(token))
}

func (r *PostgresRegistry) FindByID(ctx context.Context, id int64) (Kiosk, error) {
	if id <= 0 {
		return Kiosk{}, ErrKioskNotFound
	}
	return r.find(ctx, `SELECT id, name, company_id, is_active, session_version FROM public.kiosks WHERE id=$1`, id)
}

func (r *PostgresRegistry) find(ctx context.Context, sql string, args ...any) (Kiosk, error) {
	if r == nil || r.db == nil {
		return Kiosk{}, errors.New("kiosk database is not configured")
	}
	lease, server, err := r.db.GetPrimary(ctx)
	if err != nil {
		return Kiosk{}, fmt.Errorf("acquire kiosk database: %w", err)
	}
	defer r.db.Release(lease, server)
	var item Kiosk
	err = lease.Conn().QueryRow(ctx, sql, args...).Scan(&item.ID, &item.Name, &item.CompanyID, &item.IsActive, &item.SessionVersion)
	if errors.Is(err, ds.ErrNoRows) {
		return Kiosk{}, ErrKioskNotFound
	}
	if err != nil {
		return Kiosk{}, fmt.Errorf("read kiosk: %w", err)
	}
	return item, nil
}

// Create returns the credential once; only its digest is persisted.
func (r *PostgresRegistry) Create(ctx context.Context, name string, companyID int64) (Kiosk, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 || companyID <= 0 {
		return Kiosk{}, "", errors.New("kiosk name and positive company ID are required")
	}
	token, err := GenerateToken()
	if err != nil {
		return Kiosk{}, "", err
	}
	item, err := r.find(ctx, `INSERT INTO public.kiosks (name, company_id, token_hash) VALUES ($1,$2,$3) RETURNING id,name,company_id,is_active,session_version`, name, companyID, HashToken(token))
	if err != nil {
		return Kiosk{}, "", err
	}
	return item, token, nil
}

func (r *PostgresRegistry) Rotate(ctx context.Context, id int64) (string, error) {
	if id <= 0 {
		return "", errors.New("positive kiosk ID is required")
	}
	token, err := GenerateToken()
	if err != nil {
		return "", err
	}
	_, err = r.find(ctx, `UPDATE public.kiosks SET token_hash=$2 WHERE id=$1 RETURNING id,name,company_id,is_active,session_version`, id, HashToken(token))
	if err != nil {
		return "", err
	}
	return token, nil
}

func (r *PostgresRegistry) SetActive(ctx context.Context, id int64, active bool) error {
	if id <= 0 {
		return errors.New("positive kiosk ID is required")
	}
	_, err := r.find(ctx, `UPDATE public.kiosks SET is_active=$2 WHERE id=$1 RETURNING id,name,company_id,is_active,session_version`, id, active)
	return err
}
