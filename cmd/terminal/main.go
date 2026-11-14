// Command terminal provisions Terminal Bridge installations. It performs no
// bank, YClients, or fiscal device calls. Operator resolutions use the admin API.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dronm/pronogti/internal/config"
	"github.com/dronm/pronogti/internal/terminalbridge"
	"github.com/dronm/webapp/database"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out, errOut io.Writer) error {
	global := flag.NewFlagSet("terminal", flag.ContinueOnError)
	global.SetOutput(errOut)
	path := global.String("config", config.DefaultConfigPath, "Backend JSON configuration path")
	if err := global.Parse(args); err != nil {
		return err
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		return errors.New("usage: terminal [-config config.json] register-agent|rotate-token|register-terminal|list|status|enable-agent|disable-agent|enable-terminal|disable-terminal [flags]")
	}
	action := remaining[0]
	allowed := map[string]bool{"register-agent": true, "rotate-token": true, "register-terminal": true, "list": true, "status": true, "enable-agent": true, "disable-agent": true, "enable-terminal": true, "disable-terminal": true}
	if !allowed[action] {
		return errors.New("unknown terminal administration action")
	}
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(errOut)
	agentID := flags.String("agent", "", "Agent identity, matching the Bridge config.json")
	company := flags.Int64("company", 0, "Company ID")
	kiosk := flags.Int64("kiosk", 0, "Registered kiosk ID")
	environment := flags.String("environment", "mock", "mock or production")
	profile := flags.Int64("profile", 0, "Terminal-capable payment profile ID")
	logical := flags.String("terminal", "", "Logical terminal ID, matching the Bridge config.json")
	bankID := flags.String("bank-terminal", "", "Bank terminal identity")
	physical := flags.String("physical", "", "Stable physical-device inventory identity")
	id := flags.Int64("id", 0, "Payment terminal database ID")
	commandID := flags.String("command", "", "Terminal command UUID")
	enabled := flags.Bool("enabled", true, "Enable a newly registered agent or terminal")
	if err := flags.Parse(remaining[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected terminal command arguments")
	}
	switch action {
	case "register-agent":
		if *agentID == "" || *company <= 0 || *kiosk <= 0 {
			return errors.New("register-agent requires -agent, -company and -kiosk")
		}
	case "register-terminal":
		if *agentID == "" || *company <= 0 || *kiosk <= 0 || *profile <= 0 || *logical == "" || *bankID == "" || *physical == "" {
			return errors.New("register-terminal requires -agent, -company, -kiosk, -profile, -terminal, -bank-terminal and -physical")
		}
	case "rotate-token", "enable-agent", "disable-agent":
		if *agentID == "" {
			return errors.New("action requires -agent")
		}
	case "enable-terminal", "disable-terminal":
		if *id <= 0 {
			return errors.New("action requires a positive -id")
		}
	case "status":
		if *commandID == "" {
			return errors.New("status requires -command UUID")
		}
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	dbCfg, err := cfg.Database.Resolve()
	if err != nil {
		return err
	}
	db, err := database.Open(dbCfg, nil)
	if err != nil {
		return err
	}
	defer db.Close()
	opts := cfg.TerminalBridge
	store := terminalbridge.NewRepository(db, terminalbridge.Options{Enabled: opts.Enabled, AllowLive: opts.AllowLive, AllowSimulated: opts.AllowSimulated,
		CommandTTL: time.Duration(opts.CommandTTLSeconds) * time.Second, MaxPoll: time.Duration(opts.MaxPollSeconds) * time.Second, Lease: time.Duration(opts.LeaseSeconds) * time.Second, HeartbeatMaxAge: time.Duration(opts.HeartbeatMaxAgeSeconds) * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var result any
	switch action {
	case "register-agent":
		result, err = store.RegisterAgent(ctx, terminalbridge.AgentInput{ID: *agentID, CompanyID: *company, KioskID: *kiosk, Environment: *environment, Enabled: *enabled})
	case "rotate-token":
		result, err = store.RotateAgentToken(ctx, *agentID)
	case "register-terminal":
		result, err = store.RegisterTerminal(ctx, terminalbridge.TerminalInput{AgentID: *agentID, CompanyID: *company, KioskID: *kiosk, PaymentProfileID: *profile, TerminalID: *logical, BankTerminalID: *bankID, PhysicalID: *physical, Enabled: *enabled})
	case "status":
		result, err = store.GetCommand(ctx, *commandID)
	case "enable-agent", "disable-agent":
		err = store.SetAgentEnabled(ctx, *agentID, action == "enable-agent")
	case "enable-terminal", "disable-terminal":
		err = store.SetTerminalEnabled(ctx, *id, action == "enable-terminal")
	case "list":
		var agents []terminalbridge.Agent
		var terminals []terminalbridge.Terminal
		agents, err = store.ListAgents(ctx)
		if err == nil {
			terminals, err = store.ListTerminals(ctx)
		}
		result = map[string]any{"agents": agents, "terminals": terminals}
	}
	if err != nil {
		return err
	}
	if result == nil {
		result = map[string]bool{"ok": true}
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "\t")
	return encoder.Encode(result)
}
