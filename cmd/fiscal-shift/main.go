// Command fiscal-shift closes open KKT shifts without a browser session.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/dronm/pronogti/internal/config"
	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/webapp/database"
	"github.com/google/uuid"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("fiscal-shift", flag.ContinueOnError)
	flags.SetOutput(errOut)
	configPath := flags.String("config", config.DefaultConfigPath, "configuration JSON path")
	profileID := flags.Int64("profile", 0, "close one fiscal profile; omit with -all")
	all := flags.Bool("all", false, "close every enabled live profile, deduplicated by device")
	key := flags.String("request-key", "", "reuse this key to reconcile a previous uncertain invocation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*profileID <= 0 && !*all) || (*profileID != 0 && *all) || *profileID < 0 {
		return errors.New("usage: fiscal-shift -config config.json -profile ID | -all [-request-key KEY]")
	}
	if *key == "" {
		*key = uuid.NewString()
	}
	if !fiscal.ValidShiftKey(*key) {
		return errors.New("request-key must contain 1 to 128 bytes")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if !cfg.Fiscal.AllowLive {
		return errors.New("shift closing requires fiscal.allow_live=true")
	}
	dbConfig, err := cfg.Database.Resolve()
	if err != nil {
		return err
	}
	db, err := database.Open(dbConfig, nil)
	if err != nil {
		return fmt.Errorf("open fiscal database: %w", err)
	}
	defer db.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	service := fiscal.NewShiftService(fiscal.NewPostgresRepository(db), fiscal.HTTPDriver{}, cfg.Fiscal.AllowLive)
	// Print before dispatch so a killed process still leaves the key in its log.
	if err := json.NewEncoder(out).Encode(map[string]any{"request_key": *key, "profile_id": *profileID, "all": *all}); err != nil {
		return err
	}
	emit := func(profile fiscal.Profile, result fiscal.ShiftResult, err error) {
		entry := map[string]any{"profile_id": profile.ID, "profile_name": profile.Name, "request_key": *key, "result": result}
		if err != nil {
			entry["error"] = err.Error()
		}
		_ = json.NewEncoder(out).Encode(entry)
	}
	if *all {
		return service.CloseAll(ctx, *key, emit)
	}
	result, err := service.CloseProfile(ctx, *profileID, *key)
	id := *profileID
	emit(fiscal.Profile{ID: &id}, result, err)
	if err != nil {
		return err
	}
	if result.State == "unknown" || result.State == "failed" {
		return errors.New(result.Message)
	}
	return nil
}
