// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/user"
)

// TestGoldenrecSeedUserConvergesExistingAccounts protects the preflight contract:
// rerunning the executable must discard mutations from a prior replay rather than
// allowing credentials, policies, preferences, or active sessions to leak into it.
func TestGoldenrecSeedUserConvergesExistingAccounts(t *testing.T) {
	programData := filepath.Join(t.TempDir(), "program-data")
	runGoldenrecSeedUser(t, programData)

	ctx := context.Background()
	initialStores := openGoldenrecSeedStores(t, programData)
	initialStoresClosed := false
	t.Cleanup(func() {
		if initialStoresClosed {
			return
		}
		if err := initialStores.Close(); err != nil {
			t.Errorf("close initial seed state: %v", err)
		}
	})
	stores := initialStores
	identity := store.NewIdentity(stores)
	users := user.New(identity, clock.NewReal(), rand.Reader)
	admin, err := users.GetByName(ctx, "GoldenrecAdmin")
	if err != nil {
		t.Fatalf("load seeded administrator: %v", err)
	}
	baselineConfiguration := append([]byte(nil), admin.Configuration...)

	admin.Configuration = []byte(`{"SubtitleMode":"Always"}`)
	admin.Policy = []byte(`{"IsAdministrator":true,"IsHidden":false,"StaleReplayMutation":true}`)
	if err := users.Update(ctx, admin); err != nil {
		t.Fatalf("inject stale administrator state: %v", err)
	}
	preferenceItemID, err := dto.ParseUUID("11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("parse preference item ID: %v", err)
	}
	if err := identity.PutDisplayPreference(ctx, store.DisplayPreference{
		UserID: admin.ID,
		ItemID: preferenceItemID,
		Client: "Goldenrec stale client",
		Data:   []byte(`{"ShowSidebar":false}`),
	}); err != nil {
		t.Fatalf("inject stale display preference: %v", err)
	}
	deviceName := "Goldenrec stale device options"
	if err := identity.ReplaceAccessSchedules(ctx, admin.ID, []store.AccessSchedule{{UserID: admin.ID, DayOfWeek: 1, StartHour: 8, EndHour: 17}}); err != nil {
		t.Fatalf("inject stale access schedule: %v", err)
	}
	staleSession, err := users.IssueToken(ctx, store.Device{
		UserID:     admin.ID,
		AppName:    "Goldenrec stale app",
		AppVersion: "1",
		DeviceName: "Goldenrec stale device",
		DeviceID:   "goldenrec-stale-device",
	})
	if err != nil {
		t.Fatalf("inject stale session: %v", err)
	}
	if err := identity.PutDeviceOptions(ctx, store.DeviceOptions{DeviceID: staleSession.DeviceID, CustomName: &deviceName}); err != nil {
		t.Fatalf("inject stale device options: %v", err)
	}
	if err := users.ChangePassword(ctx, admin.ID, "stale-password"); err != nil {
		t.Fatalf("inject stale administrator password: %v", err)
	}
	err = initialStores.Close()
	initialStoresClosed = true
	if err != nil {
		t.Fatalf("close mutated seed state: %v", err)
	}

	runGoldenrecSeedUser(t, programData)

	stores = openGoldenrecSeedStores(t, programData)
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close converged seed state: %v", err)
		}
	})
	identity = store.NewIdentity(stores)
	users = user.New(identity, clock.NewReal(), rand.Reader)
	admin, err = users.GetByName(ctx, "GoldenrecAdmin")
	if err != nil {
		t.Fatalf("load converged administrator: %v", err)
	}
	assertGoldenrecCanonicalPolicy(t, admin.Policy, map[string]any{
		"EnableContentDeletion":           true,
		"EnableRemoteControlOfOtherUsers": true,
		"IsAdministrator":                 true,
		"IsHidden":                        true,
	})
	if !reflect.DeepEqual(admin.Configuration, baselineConfiguration) {
		t.Fatalf("rerun retained mutable administrator configuration: got %s, want initial seed state %s", admin.Configuration, baselineConfiguration)
	}
	if verified, err := users.VerifyPassword(ctx, admin.ID, "goldenrec-change-me"); err != nil || !verified {
		t.Fatalf("canonical administrator credential was not restored: verified=%t err=%v", verified, err)
	}
	if verified, err := users.VerifyPassword(ctx, admin.ID, "stale-password"); err != nil || verified {
		t.Fatalf("rerun retained stale administrator credential: verified=%t err=%v", verified, err)
	}
	if _, err := identity.GetDisplayPreference(ctx, admin.ID, preferenceItemID, "Goldenrec stale client"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rerun retained stale display preference: %v", err)
	}
	if _, err := users.Token(ctx, staleSession.AccessToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rerun retained stale session: %v", err)
	}
	if _, err := identity.GetDeviceOptions(ctx, staleSession.DeviceID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rerun retained stale device options: %v", err)
	}
	accessSchedules, err := identity.AccessSchedules(ctx, admin.ID)
	if err != nil {
		t.Fatalf("read access schedules after rerun: %v", err)
	}
	if len(accessSchedules) != 0 {
		t.Fatalf("rerun retained stale access schedules: %#v", accessSchedules)
	}

	disabled, err := users.GetByName(ctx, "GoldenrecDisabled")
	if err != nil {
		t.Fatalf("load converged disabled account: %v", err)
	}
	assertGoldenrecCanonicalPolicy(t, disabled.Policy, map[string]any{"IsDisabled": true})
}

func runGoldenrecSeedUser(t *testing.T, programData string) {
	t.Helper()
	credentialsFile := writeGoldenrecSeedCredentials(t, t.TempDir(), goldenrecSeedCredentialsJSON(t, goldenrecSeedCredentialValues()))
	output, err := runGoldenrecSeedUserCommand(t, programData, credentialsFile, "")
	if err != nil {
		t.Fatalf("goldenrec-seed-user rerun failed: %v", err)
	}
	if output != "" {
		t.Fatalf("goldenrec-seed-user rerun wrote unexpected output")
	}
}

func TestGoldenrecSeedUserRejectsInvalidCredentialFilesWithoutEchoingCredentials(t *testing.T) {
	values := goldenrecSeedCredentialValues()
	fieldNames := []string{"admin_name", "admin_password", "disabled_name", "disabled_password"}
	type testCase struct {
		name            string
		contents        string
		unreadable      bool
		legacyFlag      bool
		wantUnknownFlag bool
		sensitive       []string
	}
	base := goldenrecSeedCredentialsJSON(t, values)
	cases := []testCase{
		{
			name:      "malformed JSON",
			contents:  `{"admin_name":"admin-name-not-for-errors","admin_password":"admin-password-not-for-errors"`,
			sensitive: []string{"admin-name-not-for-errors", "admin-password-not-for-errors"},
		},
		{
			name:      "trailing JSON",
			contents:  base + "\n{}",
			sensitive: goldenrecSeedCredentialStrings(values),
		},
		{
			name: "unknown field",
			contents: goldenrecSeedCredentialsJSON(t, map[string]any{
				"admin_name":        values["admin_name"],
				"admin_password":    values["admin_password"],
				"disabled_name":     values["disabled_name"],
				"disabled_password": values["disabled_password"],
				"unrecognized":      "unknown-value-not-for-errors",
			}),
			sensitive: append(goldenrecSeedCredentialStrings(values), "unknown-value-not-for-errors"),
		},
		{
			name:       "unreadable file",
			contents:   base,
			unreadable: true,
			sensitive:  goldenrecSeedCredentialStrings(values),
		},
		{
			name:            "legacy password flag",
			contents:        base,
			legacyFlag:      true,
			wantUnknownFlag: true,
			sensitive:       goldenrecSeedCredentialStrings(values),
		},
	}
	for _, field := range fieldNames {
		missing := goldenrecSeedCredentialValues()
		delete(missing, field)
		cases = append(cases, testCase{
			name:      "missing " + field,
			contents:  goldenrecSeedCredentialsJSON(t, missing),
			sensitive: goldenrecSeedCredentialStrings(values),
		})
		for _, invalid := range []any{"", " \t\n", nil} {
			invalidField := goldenrecSeedCredentialValues()
			invalidField[field] = invalid
			cases = append(cases, testCase{
				name:      "invalid " + field,
				contents:  goldenrecSeedCredentialsJSON(t, invalidField),
				sensitive: goldenrecSeedCredentialStrings(values),
			})
		}
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			credentialsFile := writeGoldenrecSeedCredentials(t, t.TempDir(), test.contents)
			if test.unreadable {
				if err := os.Chmod(credentialsFile, 0); err != nil {
					t.Fatalf("make credential fixture unreadable: %v", err)
				}
			}
			mode := ""
			if test.legacyFlag {
				mode = "legacy-password-flag"
			}
			output, err := runGoldenrecSeedUserCommand(t, filepath.Join(t.TempDir(), "program-data"), credentialsFile, mode)
			if err == nil {
				t.Fatal("goldenrec-seed-user accepted invalid credential input")
			}
			if test.wantUnknownFlag {
				if !strings.Contains(output, "flag provided but not defined: -admin-password") {
					t.Fatal("legacy password flag was not rejected as unknown")
				}
			} else if !strings.Contains(strings.ToLower(output), "credential") {
				t.Fatal("credential error did not identify the credential input")
			}
			for _, value := range test.sensitive {
				if strings.Contains(output, value) {
					t.Fatal("credential value leaked in helper error")
				}
			}
		})
	}
}

func runGoldenrecSeedUserCommand(t *testing.T, programData, credentialsFile, mode string) (string, error) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestGoldenrecSeedUserHelper$")
	command.Env = append(os.Environ(),
		"GOLDENREC_SEED_USER_HELPER=1",
		"GOLDENREC_SEED_USER_PROGRAM_DATA="+programData,
		"GOLDENREC_SEED_USER_CREDENTIALS_FILE="+credentialsFile,
		"GOLDENREC_SEED_USER_MODE="+mode,
	)
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestGoldenrecSeedUserHelper(t *testing.T) {
	if os.Getenv("GOLDENREC_SEED_USER_HELPER") != "1" {
		return
	}
	args := []string{
		"goldenrec-seed-user",
		"-program-data", os.Getenv("GOLDENREC_SEED_USER_PROGRAM_DATA"),
		"-credentials-file", os.Getenv("GOLDENREC_SEED_USER_CREDENTIALS_FILE"),
	}
	if os.Getenv("GOLDENREC_SEED_USER_MODE") == "legacy-password-flag" {
		args = append(args, "-admin-password")
	}
	os.Args = args
	main()
	os.Exit(0)
}

func goldenrecSeedCredentialValues() map[string]any {
	return map[string]any{
		"admin_name":        "GoldenrecAdmin",
		"admin_password":    "goldenrec-change-me",
		"disabled_name":     "GoldenrecDisabled",
		"disabled_password": "goldenrec-disabled-password",
	}
}

func goldenrecSeedCredentialsJSON(t *testing.T, values map[string]any) string {
	t.Helper()
	document, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("marshal credential fixture: %v", err)
	}
	return string(document)
}

func goldenrecSeedCredentialStrings(values map[string]any) []string {
	credentialValues := make([]string, 0, len(values))
	for _, value := range values {
		if stringValue, ok := value.(string); ok {
			credentialValues = append(credentialValues, stringValue)
		}
	}
	return credentialValues
}

func writeGoldenrecSeedCredentials(t *testing.T, directory, contents string) string {
	t.Helper()
	credentialsFile := filepath.Join(directory, "goldenrec-credentials.json")
	if err := os.WriteFile(credentialsFile, []byte(contents), 0o600); err != nil {
		t.Fatalf("write credential fixture: %v", err)
	}
	if err := os.Chmod(credentialsFile, 0o600); err != nil {
		t.Fatalf("secure credential fixture: %v", err)
	}
	info, err := os.Stat(credentialsFile)
	if err != nil {
		t.Fatalf("stat credential fixture: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential fixture mode = %03o, want 600", info.Mode().Perm())
	}
	return credentialsFile
}

func openGoldenrecSeedStores(t *testing.T, programData string) store.Stores {
	t.Helper()
	paths, err := config.NewPaths(programData)
	if err != nil {
		t.Fatalf("derive program-data paths: %v", err)
	}
	stores, err := sqlite.Open(context.Background(), filepath.Join(paths.Data, "deadfin.db"))
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	return stores
}

func assertGoldenrecCanonicalPolicy(t *testing.T, document []byte, want map[string]any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(document, &got); err != nil {
		t.Fatalf("decode seeded policy: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("seeded policy retained stale state or diverged from canonical policy: got %s, want %s", gotJSON, wantJSON)
	}
}
