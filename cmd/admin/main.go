// Command admin provisions administrator credentials without public registration
// or passwords in process arguments.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/pronogti/internal/config"
	"github.com/dronm/webapp/database"
)

type command struct {
	configPath  string
	name        string
	passwordEnv string
	reset       bool
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseCommand(args []string, errOut io.Writer) (command, error) {
	global := flag.NewFlagSet("admin", flag.ContinueOnError)
	global.SetOutput(errOut)
	configPath := global.String("config", config.DefaultConfigPath, "configuration JSON path")
	if err := global.Parse(args); err != nil {
		return command{}, err
	}
	remaining := global.Args()
	if len(remaining) == 0 || (remaining[0] != "create" && remaining[0] != "reset-password") {
		return command{}, errors.New("usage: admin [-config config.json] create|reset-password -name NAME [-password-env VARIABLE]; otherwise read the password from stdin")
	}
	flags := flag.NewFlagSet(remaining[0], flag.ContinueOnError)
	flags.SetOutput(errOut)
	name := flags.String("name", "", "administrator login name")
	passwordEnv := flags.String("password-env", "", "environment variable containing the password; omit to read stdin")
	if err := flags.Parse(remaining[1:]); err != nil {
		return command{}, err
	}
	value := strings.TrimSpace(*name)
	if len(flags.Args()) != 0 || value == "" || utf8.RuneCountInString(value) > 50 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return command{}, errors.New("a nonempty administrator -name of at most 50 characters is required")
	}
	if *passwordEnv != "" && !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(*passwordEnv) {
		return command{}, errors.New("-password-env must name an environment variable")
	}
	return command{configPath: *configPath, name: value, passwordEnv: *passwordEnv, reset: remaining[0] == "reset-password"}, nil
}

func readPassword(input io.Reader, envName string, getenv func(string) string) (string, error) {
	if envName != "" {
		password := getenv(envName)
		if password == "" {
			return "", errors.New("the password environment variable is empty or unavailable")
		}
		return password, nil
	}
	value, err := io.ReadAll(io.LimitReader(input, 75))
	if err != nil {
		return "", errors.New("administrator password could not be read from stdin")
	}
	if len(value) >= 75 {
		return "", errors.New("password must contain between 12 and 72 UTF-8 bytes")
	}
	password := strings.TrimSuffix(string(value), "\n")
	password = strings.TrimSuffix(password, "\r")
	if password == "" {
		return "", errors.New("a password must be supplied through stdin or -password-env")
	}
	return password, nil
}

func run(args []string, input io.Reader, out, errOut io.Writer, getenv func(string) string) error {
	command, err := parseCommand(args, errOut)
	if err != nil {
		return err
	}
	password, err := readPassword(input, command.passwordEnv, getenv)
	if err != nil {
		return err
	}
	hash, err := adminauth.HashPassword(password)
	if err != nil {
		return err
	}
	cfg, err := config.Load(command.configPath)
	if err != nil {
		return err
	}
	dbConfig, err := cfg.Database.Resolve()
	if err != nil {
		return err
	}
	db, err := database.Open(dbConfig, nil)
	if err != nil {
		return fmt.Errorf("open administrator database: %w", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	user, err := adminauth.NewPostgresRepository(db).Provision(ctx, command.name, hash, command.reset)
	if err != nil {
		return err
	}
	// The output deliberately contains only the public account identity.
	return json.NewEncoder(out).Encode(user)
}
