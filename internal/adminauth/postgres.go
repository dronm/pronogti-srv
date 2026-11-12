package adminauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/dronm/ds/v4"
)

type PostgresRepository struct{ db ds.Provider }

func NewPostgresRepository(db ds.Provider) *PostgresRepository { return &PostgresRepository{db: db} }

const accountColumns = `u.id::bigint,u.name,u.role_id::text,COALESCE(u.locale_id::text,'ru'),COALESCE(u.password_hash,''),u.session_version,COALESCE(u.banned,false)`

func scanAccount(row ds.Row) (Account, error) {
	var value Account
	err := row.Scan(&value.ID, &value.Name, &value.RoleID, &value.LocaleID, &value.PasswordHash, &value.SessionVersion, &value.Banned)
	if errors.Is(err, ds.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return value, err
}

func (r *PostgresRepository) FindByName(ctx context.Context, name string) (Account, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Account{}, err
	}
	defer release()
	return scanAccount(conn.QueryRow(ctx, `SELECT `+accountColumns+` FROM public.users u WHERE lower(u.name)=lower($1)`, name))
}

func (r *PostgresRepository) OpenLogin(ctx context.Context, expected Account, sessionHash string, info RequestInfo) (int64, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	var id int64
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		// Lock against password/role/ban changes while creating login evidence.
		current, err := scanAccount(tx.QueryRow(ctx, `SELECT `+accountColumns+` FROM public.users u WHERE u.id=$1 FOR SHARE`, expected.ID))
		if err != nil {
			return err
		}
		if !validAccount(current) || current.PasswordHash != expected.PasswordHash || current.SessionVersion != expected.SessionVersion || !strings.EqualFold(current.Name, expected.Name) {
			return ErrCredentials
		}
		ua := info.UserAgent
		if len(ua) > 1024 {
			ua = ua[:1024]
		}
		userAgent, _ := json.Marshal(map[string]string{"raw": strings.ToValidUTF8(ua, "")})
		return tx.QueryRow(ctx, `INSERT INTO public.logins(date_time_in,set_date_time,ip,session_id,user_id,user_agent,headers,auth_kind)
			VALUES(now(),now(),$1,$2,$3,$4::jsonb,'{}'::jsonb,'admin') RETURNING id::bigint`, normalizedIP(info.IP), sessionHash, expected.ID, string(userAgent)).Scan(&id)
	})
	return id, err
}

func (r *PostgresRepository) CurrentAccount(ctx context.Context, userID, loginID int64, sessionHash string) (Account, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Account{}, err
	}
	defer release()
	return scanAccount(conn.QueryRow(ctx, `SELECT `+accountColumns+` FROM public.users u
		JOIN public.logins l ON l.user_id=u.id WHERE u.id=$1 AND l.id=$2 AND l.session_id=$3
		AND l.auth_kind='admin' AND l.date_time_out IS NULL`, userID, loginID, sessionHash))
}

func (r *PostgresRepository) CloseLogin(ctx context.Context, loginID, userID int64, sessionHash string) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	_, err = conn.Exec(ctx, `UPDATE public.logins SET date_time_out=COALESCE(date_time_out,now())
		WHERE id=$1 AND user_id=$2 AND session_id=$3 AND auth_kind='admin'`, loginID, userID, sessionHash)
	return err
}

func (r *PostgresRepository) ChangePassword(ctx context.Context, expected Account, loginID int64, sessionHash, passwordHash string) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		// Take the user lock before the login lock, matching password revocation.
		var id int64
		if err := tx.QueryRow(ctx, `SELECT id::bigint FROM public.users
			WHERE id=$1 AND session_version=$2 AND role_id='admin' AND NOT COALESCE(banned,false)
			FOR UPDATE`, expected.ID, expected.SessionVersion).Scan(&id); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::bigint FROM public.logins
			WHERE user_id=$1 AND id=$2 AND session_id=$3 AND auth_kind='admin' AND date_time_out IS NULL
			FOR UPDATE`, expected.ID, loginID, sessionHash).Scan(&id); err != nil {
			return err
		}
		// The users_admin_auth_before_update trigger revokes all admin logins.
		_, err := tx.Exec(ctx, `UPDATE public.users SET password_hash=$2,pwd=NULL WHERE id=$1`, expected.ID, passwordHash)
		return err
	})
	if errors.Is(err, ds.ErrNoRows) {
		return ErrCredentials
	}
	return err
}

// Provision creates a new administrator or explicitly resets an existing
// administrator's password. It never silently promotes a customer account.
func (r *PostgresRepository) Provision(ctx context.Context, name, passwordHash string, reset bool) (User, error) {
	name = strings.TrimSpace(name)
	if !validName(name) || len(passwordHash) != 60 {
		return User{}, errors.New("a valid administrator name and bcrypt password are required")
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return User{}, err
	}
	defer release()
	var user User
	query := `INSERT INTO public.users(name,role_id,password_hash,pwd,banned,locale_id) VALUES($1,'admin',$2,NULL,false,'ru')
		RETURNING id::bigint,name,role_id::text,COALESCE(locale_id::text,'ru')`
	if reset {
		query = `UPDATE public.users SET password_hash=$2,pwd=NULL WHERE lower(name)=lower($1) AND role_id='admin'
			RETURNING id::bigint,name,role_id::text,COALESCE(locale_id::text,'ru')`
	}
	err = conn.QueryRow(ctx, query, name, passwordHash).Scan(&user.ID, &user.Name, &user.RoleID, &user.LocaleID)
	if errors.Is(err, ds.ErrNoRows) {
		return User{}, errors.New("administrator not found; a customer account cannot be promoted by password reset")
	}
	var pg interface{ SQLState() string }
	if errors.As(err, &pg) && pg.SQLState() == "23505" {
		return User{}, errors.New("an account with this name already exists; use reset-password for an existing administrator")
	}
	if err != nil {
		return User{}, fmt.Errorf("provision administrator: %w", err)
	}
	return user, nil
}

func normalizedIP(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.WithZone("").String()
	}
	return ""
}

func (r *PostgresRepository) connection(ctx context.Context) (ds.Conn, func(), error) {
	if r == nil || r.db == nil {
		return nil, nil, errors.New("administrator database is not configured")
	}
	lease, server, err := r.db.GetPrimary(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire administrator database: %w", err)
	}
	return lease.Conn(), func() { r.db.Release(lease, server) }, nil
}

var _ Repository = (*PostgresRepository)(nil)
