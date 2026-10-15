// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"path/filepath"
	"testing"
)

// TestPublicCorpusRootRedirectContract loads the committed public corpus and
// scans every step without a fixed index. Exactly one anonymous GET / step
// must exist. That step must expect status 302 and carry the SHADOW:RootRedirect
// operation ID.

func TestPublicCorpusRootRedirectContract(t *testing.T) {
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "public.yaml"))
	if err != nil {
		t.Fatalf("load committed public corpus: %v", err)
	}

	var rootSteps []Step
	for _, scenario := range scenarios {
		for _, step := range scenario.Steps {
			if step.Method == "GET" && step.Path == "/" {
				rootSteps = append(rootSteps, step)
			}
		}
	}

	if len(rootSteps) != 1 {
		t.Fatalf("anonymous GET / steps = %d, want one", len(rootSteps))
	}

	step := rootSteps[0]

	if step.AuthRole != "" {
		t.Fatalf("root redirect auth_role = %q, want empty", step.AuthRole)
	}
	if step.AuthCarrier != "" {
		t.Fatalf("root redirect auth_carrier = %q, want empty", step.AuthCarrier)
	}
	if step.Expect == nil {
		t.Fatalf("root redirect expect is nil, want an expect block")
	}
	if step.Expect.Status == nil {
		t.Fatalf("root redirect expect.status is nil, want a status")
	}
	if *step.Expect.Status != 302 {
		t.Fatalf("root redirect expect.status = %d, want 302", *step.Expect.Status)
	}
	if step.OperationID != "SHADOW:RootRedirect" {
		t.Fatalf("root redirect operation_id = %q, want %q", step.OperationID, "SHADOW:RootRedirect")
	}
}
