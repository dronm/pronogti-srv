// Command kiosk provisions and revokes Pronogti kiosk devices.
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
	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/webapp/database"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out, errOut io.Writer) error {
	global := flag.NewFlagSet("kiosk", flag.ContinueOnError)
	global.SetOutput(errOut)
	configPath := global.String("config", config.DefaultConfigPath, "configuration JSON path")
	if err := global.Parse(args); err != nil {
		return err
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		return errors.New("usage: kiosk -config config.json create -name NAME -company ID | rotate -id ID | disable -id ID | enable -id ID")
	}
	action := remaining[0]
	if action != "create" && action != "rotate" && action != "disable" && action != "enable" {
		return fmt.Errorf("unknown kiosk action %q", action)
	}
	command := flag.NewFlagSet(action, flag.ContinueOnError)
	command.SetOutput(errOut)
	name := command.String("name", "", "kiosk display name")
	companyID := command.Int64("company", 0, "YClients company ID")
	id := command.Int64("id", 0, "registered kiosk ID")
	if err := command.Parse(remaining[1:]); err != nil {
		return err
	}
	if command.NArg() != 0 {
		return errors.New("unexpected positional kiosk arguments")
	}
	if action == "create" && (*name == "" || *companyID <= 0) {
		return errors.New("create requires -name and positive -company")
	}
	if action != "create" && *id <= 0 {
		return errors.New("action requires a positive -id")
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
		return fmt.Errorf("open kiosk database: %w", err)
	}
	defer db.Close()
	registry := kiosk.NewPostgresRegistry(db)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result := map[string]any{"action": action}
	switch action {
	case "create":
		item, token, err := registry.Create(ctx, *name, *companyID)
		if err != nil {
			return err
		}
		result["kiosk_id"], result["company_id"], result["name"], result["token"] = item.ID, item.CompanyID, item.Name, token
	case "rotate":
		token, err := registry.Rotate(ctx, *id)
		if err != nil {
			return err
		}
		result["kiosk_id"], result["token"] = *id, token
	case "disable", "enable":
		if err := registry.SetActive(ctx, *id, action == "enable"); err != nil {
			return err
		}
		result["kiosk_id"], result["is_active"] = *id, action == "enable"
	}
	return json.NewEncoder(out).Encode(result)
}
