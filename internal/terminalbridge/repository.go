package terminalbridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/webapp"
)

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type Repository struct {
	db      ds.Provider
	options Options
}

func NewRepository(db ds.Provider, options Options) *Repository {
	if options.CommandTTL <= 0 {
		options.CommandTTL = 120 * time.Second
	}
	if options.MaxPoll <= 0 {
		options.MaxPoll = 25 * time.Second
	}
	if options.Lease <= 0 {
		options.Lease = 45 * time.Second
	}
	if options.HeartbeatMaxAge <= 0 {
		options.HeartbeatMaxAge = 60 * time.Second
	}
	return &Repository{db: db, options: options}
}

func (r *Repository) connection(ctx context.Context) (ds.Conn, func(), error) {
	if r == nil || r.db == nil {
		return nil, nil, errors.New("terminal bridge database is not configured")
	}
	lease, server, err := r.db.GetPrimary(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire terminal bridge database: %w", err)
	}
	return lease.Conn(), func() { r.db.Release(lease, server) }, nil
}

func conflict(code, message string) error {
	return webapp.Conflict(message, map[string]any{"code": code})
}
func invalid(message string) error { return webapp.BadRequest(message, nil) }
func notFound() error              { return webapp.NotFound("terminal bridge record not found", nil) }
func unauthorized() error {
	return webapp.Unauthorized("valid terminal agent credentials are required", nil)
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func token() (string, string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	value := base64.RawURLEncoding.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(value))
	return value, hex.EncodeToString(sum[:]), nil
}

const agentColumns = `id,company_id,kiosk_id,environment,enabled,last_seen_at,COALESCE(heartbeat,'{}'::jsonb)`

func scanAgent(row ds.Row) (Agent, error) {
	var agent Agent
	err := row.Scan(&agent.ID, &agent.CompanyID, &agent.KioskID, &agent.Environment, &agent.Enabled, &agent.LastSeenAt, &agent.Heartbeat)
	return agent, err
}

func (r *Repository) RegisterAgent(ctx context.Context, input AgentInput) (AgentCredentials, error) {
	if !identityPattern.MatchString(input.ID) || input.CompanyID <= 0 || input.KioskID <= 0 || input.Environment != "mock" && input.Environment != "production" {
		return AgentCredentials{}, invalid("valid agent ID, company, kiosk and environment are required")
	}
	value, hash, err := token()
	if err != nil {
		return AgentCredentials{}, err
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return AgentCredentials{}, err
	}
	defer release()
	var result Agent
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx, `SELECT id FROM public.kiosks WHERE id=$1 AND company_id=$2 AND is_active FOR UPDATE`, input.KioskID, input.CompanyID).Scan(&id); errors.Is(err, ds.ErrNoRows) {
			return conflict("terminal_kiosk_unavailable", "agent requires an active kiosk in this company")
		} else if err != nil {
			return err
		}
		var existing string
		err := tx.QueryRow(ctx, `SELECT id FROM public.terminal_agents WHERE id=$1`, input.ID).Scan(&existing)
		if err == nil {
			return conflict("terminal_agent_exists", "agent ID already exists; use token rotation")
		}
		if !errors.Is(err, ds.ErrNoRows) {
			return err
		}
		result, err = scanAgent(tx.QueryRow(ctx, `INSERT INTO public.terminal_agents(id,company_id,kiosk_id,environment,token_hash,enabled) VALUES($1,$2,$3,$4,$5,$6) RETURNING `+agentColumns, input.ID, input.CompanyID, input.KioskID, input.Environment, hash, input.Enabled))
		return err
	})
	if err != nil {
		return AgentCredentials{}, err
	}
	return AgentCredentials{Agent: result, Token: value}, nil
}

func (r *Repository) RotateAgentToken(ctx context.Context, id string) (AgentCredentials, error) {
	value, hash, err := token()
	if err != nil {
		return AgentCredentials{}, err
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return AgentCredentials{}, err
	}
	defer release()
	agent, err := scanAgent(conn.QueryRow(ctx, `UPDATE public.terminal_agents SET token_hash=$2,updated_at=now() WHERE id=$1 RETURNING `+agentColumns, id, hash))
	if errors.Is(err, ds.ErrNoRows) {
		return AgentCredentials{}, notFound()
	}
	if err != nil {
		return AgentCredentials{}, err
	}
	return AgentCredentials{Agent: agent, Token: value}, nil
}

func (r *Repository) SetAgentEnabled(ctx context.Context, id string, enabled bool) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	result, err := conn.Exec(ctx, `UPDATE public.terminal_agents SET enabled=$2,updated_at=now() WHERE id=$1`, id, enabled)
	if err == nil && result.RowsAffected() != 1 {
		return notFound()
	}
	return err
}

func (r *Repository) Authenticate(ctx context.Context, id, value string) (Agent, error) {
	if !identityPattern.MatchString(id) || len(value) < 32 || len(value) > 256 {
		return Agent{}, unauthorized()
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Agent{}, err
	}
	defer release()
	var agent Agent
	var hash string
	err = conn.QueryRow(ctx, `SELECT `+agentColumns+`,token_hash FROM public.terminal_agents WHERE id=$1`, id).Scan(&agent.ID, &agent.CompanyID, &agent.KioskID, &agent.Environment, &agent.Enabled, &agent.LastSeenAt, &agent.Heartbeat, &hash)
	if errors.Is(err, ds.ErrNoRows) {
		return Agent{}, unauthorized()
	}
	if err != nil {
		return Agent{}, err
	}
	sum := sha256.Sum256([]byte(value))
	expected := hex.EncodeToString(sum[:])
	if !agent.Enabled || subtle.ConstantTimeCompare([]byte(hash), []byte(expected)) != 1 {
		return Agent{}, unauthorized()
	}
	agent.credentialHash = hash
	return agent, nil
}

func (r *Repository) Heartbeat(ctx context.Context, agent Agent, input Heartbeat) error {
	if len(input.Terminals) > 100 || len(input.Version) > 128 {
		return invalid("heartbeat is too large")
	}
	seen := make(map[string]bool)
	for _, status := range input.Terminals {
		if !identityPattern.MatchString(status.TerminalID) || seen[status.TerminalID] || len(status.Message) > 1000 {
			return invalid("invalid terminal heartbeat")
		}
		seen[status.TerminalID] = true
		if agent.Environment == "production" && status.Simulated {
			return conflict("terminal_simulation_forbidden", "production agent cannot advertise a simulator")
		}
		if agent.Environment == "mock" && !status.Simulated {
			return conflict("terminal_simulation_required", "mock agents must only advertise explicitly simulated terminals")
		}
	}
	if agent.Environment == "production" && input.Simulated {
		return conflict("terminal_simulation_forbidden", "production agent cannot advertise a simulator")
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	result, err := conn.Exec(ctx, `UPDATE public.terminal_agents SET heartbeat=$2::jsonb,last_seen_at=now(),updated_at=now() WHERE id=$1 AND enabled AND company_id=$3 AND kiosk_id=$4 AND environment=$5 AND token_hash=$6`, agent.ID, string(encoded), agent.CompanyID, agent.KioskID, agent.Environment, agent.credentialHash)
	if err == nil && result.RowsAffected() != 1 {
		return unauthorized()
	}
	return err
}

const terminalColumns = `id,agent_id,company_id,kiosk_id,payment_profile_id,terminal_id,bank_terminal_id,physical_id,enabled`

func scanTerminal(row ds.Row) (Terminal, error) {
	var t Terminal
	err := row.Scan(&t.ID, &t.AgentID, &t.CompanyID, &t.KioskID, &t.PaymentProfileID, &t.TerminalID, &t.BankTerminalID, &t.PhysicalID, &t.Enabled)
	return t, err
}

func (r *Repository) RegisterTerminal(ctx context.Context, input TerminalInput) (Terminal, error) {
	if !identityPattern.MatchString(input.AgentID) || !identityPattern.MatchString(input.TerminalID) || strings.TrimSpace(input.BankTerminalID) == "" || len(input.BankTerminalID) > 128 || strings.ContainsAny(input.BankTerminalID, "\r\n\x00") || strings.TrimSpace(input.PhysicalID) == "" || len(input.PhysicalID) > 300 || strings.ContainsAny(input.PhysicalID, "\r\n\x00") || input.CompanyID <= 0 || input.KioskID <= 0 || input.PaymentProfileID <= 0 {
		return Terminal{}, invalid("valid terminal identity, physical route, agent, kiosk and profile are required")
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Terminal{}, err
	}
	defer release()
	var result Terminal
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		var valid bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.terminal_agents a JOIN public.kiosks k ON k.id=a.kiosk_id AND k.company_id=a.company_id JOIN public.payment_profiles p ON p.id=$4 AND p.company_id=a.company_id AND p.environment=a.environment WHERE a.id=$1 AND a.company_id=$2 AND a.kiosk_id=$3 AND k.is_active AND p.terminals_enabled)`, input.AgentID, input.CompanyID, input.KioskID, input.PaymentProfileID).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return conflict("terminal_scope_mismatch", "terminal, agent, profile and kiosk must belong to the same company and environment")
		}
		var held bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.terminal_commands WHERE company_id=$1 AND (physical_id=$2 OR bank_terminal_id=$3) AND state IN ('queued','leased','dispatching','waiting','unknown'))`, input.CompanyID, input.PhysicalID, input.BankTerminalID).Scan(&held); err != nil {
			return err
		}
		if held {
			return conflict("terminal_busy", "physical terminal has an unresolved payment; its binding cannot be replaced")
		}
		result, err = scanTerminal(tx.QueryRow(ctx, `INSERT INTO public.payment_terminals(agent_id,company_id,kiosk_id,payment_profile_id,terminal_id,bank_terminal_id,physical_id,enabled) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+terminalColumns, input.AgentID, input.CompanyID, input.KioskID, input.PaymentProfileID, input.TerminalID, input.BankTerminalID, input.PhysicalID, input.Enabled))
		return err
	})
	return result, err
}

func (r *Repository) SetTerminalEnabled(ctx context.Context, id int64, enabled bool) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		terminal, err := scanTerminal(tx.QueryRow(ctx, `SELECT `+terminalColumns+` FROM public.payment_terminals WHERE id=$1 FOR UPDATE`, id))
		if errors.Is(err, ds.ErrNoRows) {
			return notFound()
		}
		if err != nil {
			return err
		}
		if enabled {
			var held bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.terminal_commands WHERE company_id=$1 AND (physical_id=$2 OR bank_terminal_id=$3) AND device_id<>$4 AND state IN ('queued','leased','dispatching','waiting','unknown'))`, terminal.CompanyID, terminal.PhysicalID, terminal.BankTerminalID, id).Scan(&held); err != nil {
				return err
			}
			if held {
				return conflict("terminal_busy", "another binding still holds an unresolved payment on this physical terminal")
			}
		}
		_, err = tx.Exec(ctx, `UPDATE public.payment_terminals SET enabled=$2,updated_at=now() WHERE id=$1`, id, enabled)
		return err
	})
}

func (r *Repository) ListAgents(ctx context.Context) ([]Agent, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := conn.Query(ctx, `SELECT `+agentColumns+` FROM public.terminal_agents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Agent, 0)
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func (r *Repository) ListTerminals(ctx context.Context) ([]Terminal, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := conn.Query(ctx, `SELECT `+terminalColumns+` FROM public.payment_terminals ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Terminal, 0)
	for rows.Next() {
		t, err := scanTerminal(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}
