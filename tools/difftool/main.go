// SPDX-License-Identifier: GPL-2.0-only

// Command difftool replays YAML scenarios against two Jellyfin-compatible hosts and
// reports normalized, structural API differences for the parity ledger.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"
)

type credentialHost struct {
	Username         string `json:"username"`
	Password         string `json:"password"`
	DisabledUsername string `json:"disabled_username"`
	DisabledPassword string `json:"disabled_password"`
}

func main() {
	var config Config
	var credentialsPath string
	flag.StringVar(&config.Corpus, "corpus", "", "scenario YAML file or directory")
	flag.StringVar(&config.LeftURL, "left", "", "left base URL")
	flag.StringVar(&config.RightURL, "right", "", "right base URL")
	flag.StringVar(&config.RulesDir, "rules", "", "normalizer rule directory (default: normalize beside the module)")
	flag.StringVar(&config.ResultsDir, "results", "results", "results output directory")
	flag.StringVar(&config.IssuesDir, "issues", ".diffs", "issue artifact output directory")
	flag.StringVar(&config.RunID, "run-id", "difftool", "deterministic result file stem")
	flag.StringVar(&config.Profile, "profile", "", "response profile to exercise (CamelCase)")
	flag.BoolVar(&config.CheckNormalizers, "check-normalizers", false, "replay both sides against the left stock host")
	flag.StringVar(&config.LeftAuth.Role, "left-auth-role", "default", "left bootstrap role")
	flag.StringVar(&config.RightAuth.Role, "right-auth-role", "default", "right bootstrap role")
	flag.StringVar(&credentialsPath, "credentials-file", "", "mode-0600 JSON file with left and right replay credentials")
	flag.Parse()
	if credentialsPath != "" {
		leftAuth, rightAuth, err := loadCredentialsFile(credentialsPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "difftool:", err)
			os.Exit(2)
		}
		leftAuth.Role = config.LeftAuth.Role
		rightAuth.Role = config.RightAuth.Role
		config.LeftAuth = leftAuth
		config.RightAuth = rightAuth
	}
	if _, err := Run(context.Background(), config); err != nil {
		if errors.Is(err, errDifferences) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "difftool:", err)
		os.Exit(2)
	}
}

func loadCredentialsFile(path string) (left, right AuthConfig, resultErr error) {
	file, err := openCredentialsFile(path)
	if err != nil {
		return AuthConfig{}, AuthConfig{}, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close credentials file: %w", closeErr))
		}
	}()

	decoder := json.NewDecoder(file)
	var document map[string]json.RawMessage
	if err := decoder.Decode(&document); err != nil {
		return AuthConfig{}, AuthConfig{}, fmt.Errorf("parse credentials file: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return AuthConfig{}, AuthConfig{}, errors.New("credentials file must contain one JSON object")
		}
		return AuthConfig{}, AuthConfig{}, fmt.Errorf("parse credentials file: %w", err)
	}
	for field := range document {
		if field != "left" && field != "right" {
			return AuthConfig{}, AuthConfig{}, fmt.Errorf("credentials file contains unexpected field %q", field)
		}
	}

	leftCredentials, err := parseCredentialHost(document, "left")
	if err != nil {
		return AuthConfig{}, AuthConfig{}, err
	}
	rightCredentials, err := parseCredentialHost(document, "right")
	if err != nil {
		return AuthConfig{}, AuthConfig{}, err
	}
	left, err = leftCredentials.authConfig("left")
	if err != nil {
		return AuthConfig{}, AuthConfig{}, err
	}
	right, err = rightCredentials.authConfig("right")
	if err != nil {
		return AuthConfig{}, AuthConfig{}, err
	}
	return left, right, nil
}

func parseCredentialHost(document map[string]json.RawMessage, side string) (credentialHost, error) {
	contents, ok := document[side]
	if !ok {
		return credentialHost{}, fmt.Errorf("credentials file requires %s", side)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(contents, &fields); err != nil {
		return credentialHost{}, fmt.Errorf("parse credentials file %s: %w", side, err)
	}

	var credentials credentialHost
	knownFields := map[string]*string{
		"username":          &credentials.Username,
		"password":          &credentials.Password,
		"disabled_username": &credentials.DisabledUsername,
		"disabled_password": &credentials.DisabledPassword,
	}
	for field, value := range fields {
		destination, ok := knownFields[field]
		if !ok {
			return credentialHost{}, fmt.Errorf("credentials file contains unknown field %s.%s", side, field)
		}
		if err := json.Unmarshal(value, destination); err != nil {
			return credentialHost{}, fmt.Errorf("parse credentials file %s.%s: %w", side, field, err)
		}
	}
	return credentials, nil
}

func openCredentialsFile(path string) (*os.File, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect credentials file: %w", err)
	}
	if err := validateCredentialsFileInfo(pathInfo); err != nil {
		return nil, err
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open credentials file: %w", err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, closeCredentialsFile(file, fmt.Errorf("inspect credentials file: %w", err))
	}
	if !os.SameFile(pathInfo, fileInfo) {
		return nil, closeCredentialsFile(file, errors.New("credentials file changed while opening"))
	}
	if err := validateCredentialsFileInfo(fileInfo); err != nil {
		return nil, closeCredentialsFile(file, err)
	}
	return file, nil
}

func closeCredentialsFile(file *os.File, resultErr error) error {
	if closeErr := file.Close(); closeErr != nil {
		return errors.Join(resultErr, fmt.Errorf("close credentials file: %w", closeErr))
	}
	return resultErr
}

func validateCredentialsFileInfo(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return errors.New("credentials file must be a regular, non-symlink file")
	}
	if info.Mode().Perm() != 0o600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return errors.New("credentials file must have mode 0600")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || os.Getuid() < 0 {
		return errors.New("credentials file owner cannot be verified")
	}
	if stat.Uid != uint32(os.Getuid()) {
		return errors.New("credentials file must be owned by the current user")
	}
	return nil
}

func (credentials credentialHost) authConfig(side string) (AuthConfig, error) {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "username", value: credentials.Username},
		{name: "password", value: credentials.Password},
		{name: "disabled_username", value: credentials.DisabledUsername},
		{name: "disabled_password", value: credentials.DisabledPassword},
	} {
		if field.value == "" {
			return AuthConfig{}, fmt.Errorf("credentials file requires %s.%s", side, field.name)
		}
	}
	return AuthConfig{
		Username:         credentials.Username,
		Password:         credentials.Password,
		DisabledUsername: credentials.DisabledUsername,
		DisabledPassword: credentials.DisabledPassword,
	}, nil
}
