// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestScenarioValidationRejectsSchemeRelativeStepPath(t *testing.T) {
	scenario := Scenario{
		Format: scenarioFormat,
		Name:   "reject authority override",
		Steps: []Step{{
			Name:        "must stay on configured host",
			Method:      "GET",
			Path:        "//attacker.example/Items",
			OperationID: "GetItems",
		}},
	}

	err := scenario.validate("scheme-relative.yaml")
	if err == nil {
		t.Fatal("scenario validation must reject a scheme-relative path that overrides the configured host")
	}
	if !strings.Contains(err.Error(), "path") {
		t.Fatalf("scheme-relative path error must identify the invalid path: %v", err)
	}
}

func TestScenarioValidationRequiresOperationID(t *testing.T) {
	scenario := Scenario{
		Format: scenarioFormat,
		Name:   "ledger traceability",
		Steps: []Step{{
			Name:   "unidentified operation",
			Method: "GET",
			Path:   "/Items",
		}},
	}

	err := scenario.validate("missing-operation-id.yaml")
	if err == nil {
		t.Fatal("scenario validation must reject a step without an operation_id before it can produce an untraceable result row")
	}
	if !strings.Contains(err.Error(), "operation_id") {
		t.Fatalf("missing operation ID error must identify operation_id: %v", err)
	}
}

func TestScenarioValidationRejectsOverflowingBodyLength(t *testing.T) {
	scenarioFor := func(length int) Scenario {
		return Scenario{
			Format: scenarioFormat,
			Name:   "bounded reader limit",
			Steps: []Step{{
				Name:        "body length at reader bound",
				Method:      "GET",
				Path:        "/Items",
				OperationID: "GetItems",
				Expect:      &Expect{BodyLength: &length},
			}},
		}
	}

	err := scenarioFor(math.MaxInt).validate("body-length-overflow.yaml")
	if err == nil {
		t.Fatal("scenario validation must reject an expect.body_length that overflows the reader's limit+1 bound")
	}
	if !strings.Contains(err.Error(), "expect.body_length") {
		t.Fatalf("overflow error must name expect.body_length: %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(math.MaxInt-1)) {
		t.Fatalf("overflow error must state the supported maximum %d: %v", math.MaxInt-1, err)
	}

	if err := scenarioFor(math.MaxInt - 1).validate("body-length-max.yaml"); err != nil {
		t.Fatalf("scenario validation must accept expect.body_length of math.MaxInt-1: %v", err)
	}
}
