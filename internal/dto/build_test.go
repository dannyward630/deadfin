// SPDX-License-Identifier: GPL-2.0-only

package dto_test

import (
	"encoding/json"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/dto"
)

func TestBuildInfoRendersPascalAndCamelKeys(t *testing.T) {
	body := dto.BuildInfo{Component: "deadfin-server", Version: "v9.9.9", Channel: "release", Revision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CompatibilityTarget: "12.0.0", APIVersion: 1, Capabilities: []string{"build-info"}}

	pascal, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	const wantPascal = `{"Component":"deadfin-server","Version":"v9.9.9","Channel":"release","Revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Dirty":false,"BuildTime":"","CompatibilityTarget":"12.0.0","ApiVersion":1,"Capabilities":["build-info"]}`
	if string(pascal) != wantPascal {
		t.Fatalf("json.Marshal = %s\nwant %s", pascal, wantPascal)
	}

	camel, err := render.MarshalCamel(body)
	if err != nil {
		t.Fatal(err)
	}
	const wantCamel = `{"component":"deadfin-server","version":"v9.9.9","channel":"release","revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","dirty":false,"buildTime":"","compatibilityTarget":"12.0.0","apiVersion":1,"capabilities":["build-info"]}`
	if string(camel) != wantCamel {
		t.Fatalf("MarshalCamel = %s\nwant %s", camel, wantCamel)
	}
}
