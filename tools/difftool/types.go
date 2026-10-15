// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const scenarioFormat = "goldenrec-scenario/v1"

type Scenario struct {
	Format     string `yaml:"format"`
	Name       string `yaml:"name"`
	BaseURLEnv string `yaml:"base_url_env"`
	Steps      []Step `yaml:"steps"`
}

type Expect struct {
	Status      *int              `yaml:"status"`
	BodyBytes   string            `yaml:"body_bytes"`
	BodyLength  *int              `yaml:"body_length"`
	AssertEqual map[string]string `yaml:"assert_equal"`
}

type Step struct {
	Name             string            `yaml:"name"`
	Method           string            `yaml:"method"`
	Path             string            `yaml:"path"`
	Params           map[string]any    `yaml:"params"`
	Headers          map[string]string `yaml:"headers"`
	Body             any               `yaml:"body"`
	BodyFile         string            `yaml:"body_file"`
	BodyFileField    string            `yaml:"body_file_field"`
	AuthRole         string            `yaml:"auth_role"`
	AuthCarrier      AuthCarrier       `yaml:"auth_carrier"`
	Capture          map[string]string `yaml:"capture"`
	Normalize        []string          `yaml:"normalize"`
	Normalizers      []string          `yaml:"normalizers"`
	OperationID      string            `yaml:"operation_id"`
	Expect           *Expect           `yaml:"expect"`
	authCarrierSet   bool
	bodyFileLoaded   bool
	itemIDsRewritten bool
}

type AuthCarrier string

const (
	AuthCarrierAuthorization      AuthCarrier = "authorization"
	AuthCarrierXEmbyAuthorization AuthCarrier = "x_emby_authorization"
	AuthCarrierXEmbyToken         AuthCarrier = "x_emby_token"
	AuthCarrierXMediaBrowserToken AuthCarrier = "x_mediabrowser_token"
	AuthCarrierQueryAPIKey        AuthCarrier = "query_api_key"
	AuthCarrierQueryAPIKeyLower   AuthCarrier = "query_api_key_lower"
)

func parseAuthCarrier(value AuthCarrier) (AuthCarrier, error) {
	if value == "" {
		return AuthCarrierAuthorization, nil
	}
	switch value {
	case AuthCarrierAuthorization, AuthCarrierXEmbyAuthorization, AuthCarrierXEmbyToken, AuthCarrierXMediaBrowserToken, AuthCarrierQueryAPIKey, AuthCarrierQueryAPIKeyLower:
		return value, nil
	default:
		return "", fmt.Errorf("unknown auth carrier %q", value)
	}
}

func (s *Step) UnmarshalYAML(node *yaml.Node) error {
	type rawStep Step
	var raw rawStep
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*s = Step(raw)
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == "auth_carrier" {
			s.authCarrierSet = true
			break
		}
	}
	return nil
}

func (s Scenario) validate(source string) error {
	if s.Format != scenarioFormat {
		return fmt.Errorf("%s: format must be %q", source, scenarioFormat)
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("%s: scenario name is required", source)
	}
	if len(s.Steps) == 0 {
		return fmt.Errorf("%s: scenario must contain at least one step", source)
	}
	priorCaptures := make(map[string]bool)
	for i, step := range s.Steps {
		if strings.TrimSpace(step.Name) == "" || strings.TrimSpace(step.Method) == "" {
			return fmt.Errorf("%s: step %d requires name and method", source, i+1)
		}
		if err := validateStepPath(step.Path); err != nil {
			return fmt.Errorf("%s: step %d path: %w", source, i+1, err)
		}
		if strings.TrimSpace(step.OperationID) == "" {
			return fmt.Errorf("%s: step %d requires operation_id", source, i+1)
		}
		if step.BodyFile != "" && step.Body != nil && !step.bodyFileLoaded {
			return fmt.Errorf("%s: step %d must not specify both body and body_file", source, i+1)
		}
		if step.BodyFileField != "" && step.BodyFile == "" {
			return fmt.Errorf("%s: step %d body_file_field requires body_file", source, i+1)
		}
		if step.authCarrierSet && step.AuthCarrier == "" {
			return fmt.Errorf("%s: step %d: unknown auth carrier %q", source, i+1, step.AuthCarrier)
		}
		if _, err := parseAuthCarrier(step.AuthCarrier); err != nil {
			return fmt.Errorf("%s: step %d: %w", source, i+1, err)
		}
		if err := validateCapture(step.Capture); err != nil {
			return fmt.Errorf("%s: step %d: %w", source, i+1, err)
		}
		if step.Expect != nil {
			if step.Expect.Status != nil && (*step.Expect.Status < http.StatusContinue || *step.Expect.Status > 599) {
				return fmt.Errorf("%s: step %d expect.status must be between 100 and 599", source, i+1)
			}
			if step.Expect.BodyBytes != "" && step.Expect.BodyLength != nil {
				return fmt.Errorf("%s: step %d expect.body_bytes and expect.body_length are mutually exclusive", source, i+1)
			}
			if step.Expect.BodyLength != nil && *step.Expect.BodyLength < 0 {
				return fmt.Errorf("%s: step %d expect.body_length must be non-negative", source, i+1)
			}
			if step.Expect.BodyLength != nil && *step.Expect.BodyLength > math.MaxInt-1 {
				return fmt.Errorf("%s: step %d expect.body_length must not exceed %d", source, i+1, math.MaxInt-1)
			}
			if err := validateAssertEqual(step.Expect.AssertEqual, priorCaptures); err != nil {
				return fmt.Errorf("%s: step %d: %w", source, i+1, err)
			}
		}
		for name := range step.Capture {
			priorCaptures[name] = true
		}
	}
	return nil
}

func validateStepPath(path string) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return fmt.Errorf("must be a single-leading-slash path")
	}
	ref, err := url.Parse(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	if ref.IsAbs() || ref.Host != "" || ref.User != nil || ref.Opaque != "" {
		return fmt.Errorf("must not override the configured host")
	}
	return nil
}

// validateAssertEqual checks that each capture-equality assertion names a
// response path plus a capture variable produced by an earlier step in the
// same scenario. An assertion without a prior capture is a corpus defect.
// It cannot pass. A literal default can mask a divergence.
func validateAssertEqual(assertions map[string]string, priorCaptures map[string]bool) error {
	for _, responsePath := range sortedCaptureNames(assertions) {
		if strings.TrimSpace(responsePath) == "" {
			return fmt.Errorf("assert_equal response path is required")
		}
		if _, err := parseCapturePath(responsePath); err != nil {
			return fmt.Errorf("assert_equal response path %q: %w", responsePath, err)
		}
		captureName := assertions[responsePath]
		if !captureVariableName.MatchString(captureName) {
			return fmt.Errorf("assert_equal %q references invalid capture variable %q", responsePath, captureName)
		}
		if !priorCaptures[captureName] {
			return fmt.Errorf("assert_equal %q references capture %q that no prior step captured", responsePath, captureName)
		}
	}
	return nil
}

func (s Step) selectedRules() []string {
	if len(s.Normalizers) != 0 {
		return s.Normalizers
	}
	return s.Normalize
}

type RuleFile struct {
	Version string `yaml:"version"`
	Rules   []Rule `yaml:"rules"`
}

type Rule struct {
	Name         string `yaml:"name"`
	Action       string `yaml:"action"`
	Reason       string `yaml:"reason"`
	DivergenceID string `yaml:"divergence_id"`
	Header       string `yaml:"header"`
	Seconds      int    `yaml:"seconds"`
	Pattern      string `yaml:"pattern"`
	Replacement  string `yaml:"replacement"`
	ExplicitOnly bool   `yaml:"explicit_only"`
}

var divergenceIDPattern = regexp.MustCompile(`^D-[0-9]+$`)

func (r Rule) validate(knownDivergences string) error {
	if strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.Action) == "" {
		return errors.New("normalizer rules require name and action")
	}
	if strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("normalizer rule %q is missing required reason", r.Name)
	}
	if r.DivergenceID != "" {
		if !divergenceIDPattern.MatchString(r.DivergenceID) {
			return fmt.Errorf("normalizer rule %q has invalid divergence_id %q", r.Name, r.DivergenceID)
		}
		if !strings.Contains(knownDivergences, "| "+r.DivergenceID+" |") {
			return fmt.Errorf("normalizer rule %q references unknown divergence_id %q", r.Name, r.DivergenceID)
		}
	}
	switch r.Action {
	case "server_id", "item_ids", "entity_tags", "item_created_dates", "user_data_keys", "item_user_data_playback_timestamps", "user_view_ids", "environment_fields", "full_system_runtime_capabilities", "sort_json_keys", "play_session_ids", "auth_tokens", "startup_user_name", "transcoding_url_tokens", "login_session_volatiles", "library_folder_ids", "library_options_defaults", "library_options_persisted", "quickconnect_volatiles", "problem_details_trace", "external_id_infos", "remote_search_result_order":
	case "drop_header":
		if strings.TrimSpace(r.Header) == "" {
			return fmt.Errorf("normalizer rule %q: drop_header requires header", r.Name)
		}
	case "date_jitter":
		if r.Seconds <= 0 {
			return fmt.Errorf("normalizer rule %q: date_jitter requires positive seconds", r.Name)
		}
	case "playlist_replace":
		if r.Pattern == "" {
			return fmt.Errorf("normalizer rule %q: playlist_replace requires pattern", r.Name)
		}
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf("normalizer rule %q: invalid playlist pattern: %w", r.Name, err)
		}
	default:
		return fmt.Errorf("normalizer rule %q has unsupported action %q", r.Name, r.Action)
	}
	return nil
}

type AuthConfig struct {
	Role             string
	Username         string
	Password         string
	DisabledUsername string
	DisabledPassword string
}

type Config struct {
	Corpus           string
	LeftURL          string
	RightURL         string
	RulesDir         string
	ResultsDir       string
	IssuesDir        string
	RunID            string
	Profile          string
	CheckNormalizers bool
	LeftAuth         AuthConfig
	RightAuth        AuthConfig
	Client           *http.Client
}

type Result struct {
	Format string       `json:"format"`
	RunID  string       `json:"run_id"`
	Steps  []StepResult `json:"steps"`
}

type StepResult struct {
	Scenario          string `json:"scenario"`
	Step              string `json:"step"`
	OperationID       string `json:"operation-id"`
	Status            string `json:"status"`
	BodyLengthCounted int    `json:"body_length_counted,omitempty"`
	Issue             string `json:"issue,omitempty"`
}

type Host interface {
	Request(context.Context, Step, string) (Response, error)
	BuildItemMap(context.Context) (map[string]string, error)
	SetItemIDs(map[string]string)
}

type Response struct {
	Status              int
	Headers             http.Header
	Body                []byte
	BodyLengthCounted   int
	BodyLengthOverLimit bool
	BodyLengthMode      bool
}

type normalizedResponse struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers"`
	Body    any                 `json:"body"`
	IsJSON  bool                `json:"-"`
}

type normalizer struct {
	rules      []Rule
	dateJitter time.Duration
}
