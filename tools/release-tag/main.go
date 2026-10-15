// SPDX-License-Identifier: GPL-2.0-only

// Command release-tag creates the Gitea tag and release for a merge to
// master.
//
// The root VERSION file is the only version source. The tool creates tag
// v<VERSION> whose target is the given commit SHA, then creates a non-draft,
// non-prerelease Gitea release from that tag. Reruns are safe: an existing
// tag is accepted only when it already resolves to the same commit, and an
// existing release for that tag is success. A tag that targets another
// commit is a forgotten VERSION bump and fails without any mutation.
//
// The token reaches only the forge base URL and is never printed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// versionPattern accepts exactly X.Y.Z with no leading v, sign, or padding.
var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// shaPattern accepts a full 40-character lowercase Git SHA-1.
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func main() {
	if err := run(os.Args[1:], os.Getenv("FORGE_TOKEN"), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "release-tag: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, token string, out io.Writer) error {
	fs := flag.NewFlagSet("release-tag", flag.ContinueOnError)
	fs.SetOutput(out)
	forge := fs.String("forge", "", "forge base URL, for example https://git.example.org")
	repo := fs.String("repo", "", "repository as owner/name")
	sha := fs.String("sha", "", "full commit SHA the tag must target")
	versionFile := fs.String("version-file", "VERSION", "path of the VERSION file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *forge == "" || *repo == "" || *sha == "" {
		return errors.New("-forge, -repo, and -sha are required")
	}
	if !shaPattern.MatchString(*sha) {
		return fmt.Errorf("-sha %q is not a full lowercase commit SHA", *sha)
	}
	if strings.Count(*repo, "/") != 1 || strings.HasPrefix(*repo, "/") || strings.HasSuffix(*repo, "/") {
		return fmt.Errorf("-repo %q is not owner/name", *repo)
	}
	base, err := url.Parse(*forge)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return fmt.Errorf("-forge %q is not an absolute URL", *forge)
	}
	if token == "" {
		return errors.New("FORGE_TOKEN is empty: refusing to run before any mutation")
	}
	raw, err := os.ReadFile(*versionFile)
	if err != nil {
		return err
	}
	version, err := parseVersion(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", *versionFile, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := &client{
		http:  &http.Client{Timeout: 30 * time.Second},
		base:  strings.TrimSuffix(base.String(), "/"),
		repo:  *repo,
		token: token,
	}
	return release(ctx, c, "v"+version, *sha, out)
}

// parseVersion returns the X.Y.Z version held by a VERSION file. It rejects
// an empty file, a leading v, surrounding whitespace, and extra lines. One
// trailing newline is the only tolerated decoration.
func parseVersion(raw []byte) (string, error) {
	s := string(raw)
	if s == "" {
		return "", errors.New("VERSION is empty")
	}
	s = strings.TrimSuffix(s, "\n")
	if strings.ContainsAny(s, "\r\n") {
		return "", errors.New("VERSION must hold exactly one line")
	}
	if strings.TrimSpace(s) != s {
		return "", errors.New("VERSION must not contain whitespace")
	}
	if strings.HasPrefix(s, "v") || strings.HasPrefix(s, "V") {
		return "", fmt.Errorf("VERSION %q must not carry a leading v; the tag adds it", s)
	}
	if !versionPattern.MatchString(s) {
		return "", fmt.Errorf("VERSION %q is not an X.Y.Z semantic version", s)
	}
	return s, nil
}

// release performs the idempotent tag-then-release sequence.
func release(ctx context.Context, c *client, tag, sha string, out io.Writer) error {
	target, found, err := c.tagTarget(ctx, tag)
	if err != nil {
		return err
	}
	switch {
	case found && target != sha:
		return fmt.Errorf("tag %s already exists and targets %s, not %s: bump VERSION before merging to master", tag, target, sha)
	case found:
		fmt.Fprintf(out, "tag %s already targets %s\n", tag, sha)
	default:
		if err := c.createTag(ctx, tag, sha); err != nil {
			return err
		}
		fmt.Fprintf(out, "created tag %s at %s\n", tag, sha)
	}

	id, found, err := c.releaseID(ctx, tag)
	if err != nil {
		return err
	}
	if found {
		fmt.Fprintf(out, "release %s already exists (id %d)\n", tag, id)
		return nil
	}
	id, err = c.createRelease(ctx, tag, sha)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "created release %s (id %d)\n", tag, id)
	return nil
}

type client struct {
	http  *http.Client
	base  string
	repo  string
	token string
}

func (c *client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v1/repos/"+c.repo+path, payload)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp.StatusCode, data, nil
}

func apiError(method, path string, status int, data []byte) error {
	msg := strings.TrimSpace(string(data))
	if len(msg) > 512 {
		msg = msg[:512]
	}
	return fmt.Errorf("%s %s: HTTP %d: %s", method, path, status, msg)
}

// tagTarget returns the commit SHA an existing tag resolves to.
func (c *client) tagTarget(ctx context.Context, tag string) (string, bool, error) {
	path := "/tags/" + url.PathEscape(tag)
	status, data, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", false, err
	}
	switch status {
	case http.StatusNotFound:
		return "", false, nil
	case http.StatusOK:
	default:
		return "", false, apiError(http.MethodGet, path, status, data)
	}
	var t struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return "", false, fmt.Errorf("GET %s: %w", path, err)
	}
	if t.Commit.SHA == "" {
		return "", false, fmt.Errorf("GET %s: response carries no commit sha", path)
	}
	return t.Commit.SHA, true, nil
}

func (c *client) createTag(ctx context.Context, tag, sha string) error {
	body := map[string]string{
		"tag_name": tag,
		"target":   sha,
		"message":  "Release " + tag,
	}
	status, data, err := c.do(ctx, http.MethodPost, "/tags", body)
	if err != nil {
		return err
	}
	if status != http.StatusCreated {
		return apiError(http.MethodPost, "/tags", status, data)
	}
	return nil
}

// releaseID returns the id of an existing release for the tag.
func (c *client) releaseID(ctx context.Context, tag string) (int64, bool, error) {
	path := "/releases/tags/" + url.PathEscape(tag)
	status, data, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, false, err
	}
	switch status {
	case http.StatusNotFound:
		return 0, false, nil
	case http.StatusOK:
	default:
		return 0, false, apiError(http.MethodGet, path, status, data)
	}
	var r struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return 0, false, fmt.Errorf("GET %s: %w", path, err)
	}
	return r.ID, true, nil
}

func (c *client) createRelease(ctx context.Context, tag, sha string) (int64, error) {
	body := map[string]any{
		"tag_name":         tag,
		"target_commitish": sha,
		"name":             tag,
		"body":             "Release " + tag + " from commit " + sha + ".",
		"draft":            false,
		"prerelease":       false,
	}
	status, data, err := c.do(ctx, http.MethodPost, "/releases", body)
	if err != nil {
		return 0, err
	}
	if status != http.StatusCreated {
		return 0, apiError(http.MethodPost, "/releases", status, data)
	}
	var r struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return 0, fmt.Errorf("POST /releases: %w", err)
	}
	return r.ID, nil
}
