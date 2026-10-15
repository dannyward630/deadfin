// SPDX-License-Identifier: GPL-2.0-only

// goldenrec-seed-user creates deterministic disposable accounts for differential-login replay.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/user"
)

func main() {
	if err := run(); err != nil {
		fatalf("%v", err)
	}
}

func run() (err error) {
	programData := flag.String("program-data", "", "disposable Deadfin program-data root")
	credentialsFile := flag.String("credentials-file", "", "JSON file containing deterministic seed-user credentials")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *programData == "" || *credentialsFile == "" {
		return errors.New("-program-data and -credentials-file are required")
	}
	credentials, err := loadCredentials(*credentialsFile)
	if err != nil {
		return err
	}

	ctx := context.Background()
	paths, err := config.NewPaths(*programData)
	if err != nil {
		return fmt.Errorf("derive program-data paths: %w", err)
	}
	if err := os.MkdirAll(paths.Data, 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	stores, err := sqlite.Open(ctx, filepath.Join(paths.Data, "deadfin.db"))
	if err != nil {
		return fmt.Errorf("open stores: %w", err)
	}
	defer func() {
		if closeErr := stores.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close stores: %w", closeErr)
		}
	}()

	identity := store.NewIdentity(stores)
	users := user.New(identity, clock.NewReal(), rand.Reader)
	if err := convergeUser(ctx, users, identity, credentials.AdminName, credentials.AdminPassword, true, false); err != nil {
		return err
	}
	if err := convergeUser(ctx, users, identity, credentials.DisabledName, credentials.DisabledPassword, false, true); err != nil {
		return err
	}
	return nil
}

type seedCredentials struct {
	AdminName        string `json:"admin_name"`
	AdminPassword    string `json:"admin_password"`
	DisabledName     string `json:"disabled_name"`
	DisabledPassword string `json:"disabled_password"`
}

func loadCredentials(path string) (seedCredentials, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return seedCredentials{}, errors.New("read credentials file")
	}

	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var credentials seedCredentials
	if err := decoder.Decode(&credentials); err != nil {
		return seedCredentials{}, errors.New("invalid credentials file")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return seedCredentials{}, errors.New("invalid credentials file")
	}
	if strings.TrimSpace(credentials.AdminName) == "" ||
		strings.TrimSpace(credentials.AdminPassword) == "" ||
		strings.TrimSpace(credentials.DisabledName) == "" ||
		strings.TrimSpace(credentials.DisabledPassword) == "" {
		return seedCredentials{}, errors.New("credentials file has missing or empty required fields")
	}
	return credentials, nil
}

func ensureUser(ctx context.Context, users *user.Service, name, password string) (store.User, error) {
	existing, err := users.GetByName(ctx, name)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.User{}, fmt.Errorf("find %q: %w", name, err)
	}
	created, err := users.Create(ctx, name, password)
	if err != nil {
		return store.User{}, fmt.Errorf("create %q: %w", name, err)
	}
	return created, nil
}

func convergeUser(ctx context.Context, users *user.Service, identity *store.Identity, name, password string, administrator, disabled bool) error {
	account, err := ensureUser(ctx, users, name, password)
	if err != nil {
		return err
	}
	if err := users.ChangePassword(ctx, account.ID, password); err != nil {
		return fmt.Errorf("set password for %q: %w", name, err)
	}
	account, err = users.Get(ctx, account.ID)
	if err != nil {
		return fmt.Errorf("reload %q: %w", name, err)
	}
	policy, err := canonicalPolicy(administrator, disabled)
	if err != nil {
		return fmt.Errorf("encode policy for %q: %w", name, err)
	}
	account.Name = name
	account.Policy = policy
	account.Configuration = []byte("{}")
	account.LastLoginDate = nil
	account.LastActivityDate = nil
	if err := users.Update(ctx, account); err != nil {
		return fmt.Errorf("reset account %q: %w", name, err)
	}
	if err := identity.ClearUserVolatileState(ctx, account.ID); err != nil {
		return fmt.Errorf("reset volatile state for %q: %w", name, err)
	}
	return nil
}

func canonicalPolicy(administrator, disabled bool) ([]byte, error) {
	policy := map[string]any{}
	if administrator {
		policy["IsAdministrator"] = true
		policy["IsHidden"] = true
		policy["EnableContentDeletion"] = true
		policy["EnableRemoteControlOfOtherUsers"] = true
	}
	if disabled {
		policy["IsDisabled"] = true
	}
	return json.Marshal(policy)
}

func fatalf(format string, values ...any) {
	fmt.Fprintf(os.Stderr, "goldenrec-seed-user: "+format+"\n", values...)
	os.Exit(1)
}
