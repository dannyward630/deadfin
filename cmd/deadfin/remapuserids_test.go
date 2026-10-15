// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

func TestRunRemapUserIDs(t *testing.T) {
	ctx := t.Context()
	programData := t.TempDir()
	paths, err := config.NewPaths(programData)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Data, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC)
	jellyfinID, err := dto.ParseUUID("4e703b0e6e784ea69c4843a2654c5cb1")
	if err != nil {
		t.Fatal(err)
	}
	stores, err := sqlite.Open(ctx, filepath.Join(paths.Data, databaseFile))
	if err != nil {
		t.Fatal(err)
	}
	identity := store.NewIdentity(stores)
	if _, err := identity.CreateUser(ctx, store.User{ID: jellyfinID, Name: "whiskey", NormalizedName: "WHISKEY"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.CreateDevice(ctx, store.Device{UserID: jellyfinID, AccessToken: "token", AppName: "Swiftfin", AppVersion: "1", DeviceName: "phone", DeviceID: "phone", DateCreated: now, DateModified: now, DateLastActivity: now}); err != nil {
		t.Fatal(err)
	}
	if err := stores.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runRemapUserIDs(ctx, &out, programData, "", clock.NewFake(now), true); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out.String(), "dry run") || !strings.Contains(out.String(), `user "whiskey": `+jellyfinID.String()) {
		t.Fatalf("dry-run output = %q", out.String())
	}

	out.Reset()
	if err := runRemapUserIDs(ctx, &out, programData, "", clock.NewFake(now), false); err != nil {
		t.Fatalf("remap: %v", err)
	}
	if !strings.Contains(out.String(), "devices.user_id: 1 rows") {
		t.Fatalf("output = %q", out.String())
	}

	out.Reset()
	if err := runRemapUserIDs(ctx, &out, programData, "", clock.NewFake(now.Add(time.Hour)), false); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Fatalf("second-run output = %q", out.String())
	}

	stores, err = sqlite.Open(ctx, filepath.Join(paths.Data, databaseFile))
	if err != nil {
		t.Fatal(err)
	}
	defer stores.Close()
	identity = store.NewIdentity(stores)
	device, err := identity.DeviceByToken(ctx, "token")
	if err != nil {
		t.Fatal(err)
	}
	user, err := identity.User(ctx, device.UserID)
	if err != nil || user.Name != "whiskey" || user.ID == jellyfinID {
		t.Fatalf("token owner = %+v, %v; want whiskey under a new id", user, err)
	}
}

func TestRunRemapUserIDsRequiresDatabase(t *testing.T) {
	if err := runRemapUserIDs(t.Context(), &bytes.Buffer{}, t.TempDir(), "", clock.NewFake(time.Time{}), false); err == nil {
		t.Fatal("remap created a database where none existed")
	}
}
