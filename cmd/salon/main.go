// Command salon manages the public directory of YClients companies.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dronm/pronogti/internal/config"
	"github.com/dronm/pronogti/internal/salon"
	"github.com/dronm/webapp/database"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out, errOut io.Writer) error {
	global := flag.NewFlagSet("salon", flag.ContinueOnError)
	global.SetOutput(errOut)
	configPath := global.String("config", config.DefaultConfigPath, "configuration JSON path")
	if err := global.Parse(args); err != nil {
		return err
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		return errors.New("usage: salon -config config.json upsert -company ID -slug SLUG -name NAME | enable -company ID | disable -company ID")
	}
	action := remaining[0]
	if action != "upsert" && action != "enable" && action != "disable" {
		return fmt.Errorf("unknown salon action %q", action)
	}
	command := flag.NewFlagSet(action, flag.ContinueOnError)
	command.SetOutput(errOut)
	companyID := command.Int64("company", 0, "YClients company ID")
	slug := command.String("slug", "", "public lowercase salon slug")
	name := command.String("name", "", "public salon display name")
	if err := command.Parse(remaining[1:]); err != nil {
		return err
	}
	if command.NArg() != 0 {
		return errors.New("unexpected positional salon arguments")
	}
	if *companyID <= 0 {
		return errors.New("positive -company is required")
	}
	if action == "upsert" {
		if err := salon.Validate(*companyID, strings.TrimSpace(*slug), strings.TrimSpace(*name)); err != nil {
			return err
		}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	dbConfig, err := cfg.Database.Resolve()
	if err != nil {
		return err
	}
	db, err := database.Open(dbConfig, nil)
	if err != nil {
		return fmt.Errorf("open salon database: %w", err)
	}
	defer db.Close()
	registry := salon.NewPostgresRegistry(db)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result := map[string]any{"action": action, "company_id": *companyID}
	if action == "upsert" {
		item, err := registry.Upsert(ctx, *companyID, *slug, *name)
		if err != nil {
			return err
		}
		result["salon"], result["is_active"] = item.SalonIdentity, item.IsActive
	} else {
		active := action == "enable"
		if err := registry.SetActive(ctx, *companyID, active); err != nil {
			return err
		}
		result["is_active"] = active
	}
	return json.NewEncoder(out).Encode(result)
}
