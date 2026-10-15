// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"

	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/config"
)

// newSetupGate builds the first-run gate (D-53) with a fresh one-time token
// from random. While setup is pending it logs the unlock link, so an operator
// who reaches the server through a proxy or from outside the local network can
// still run the wizard. The token lives only in this process; a restart issues
// a new one. A completed installation logs nothing and the gate stays inert.
func newSetupGate(manager *config.Manager, network middleware.SetupNetwork, random io.Reader, logger *slog.Logger) (*middleware.SetupGate, error) {
	secret := make([]byte, 32)
	if _, err := io.ReadFull(random, secret); err != nil {
		return nil, fmt.Errorf("generate setup token: %w", err)
	}
	token := hex.EncodeToString(secret)
	gate := middleware.NewSetupGate(manager, network, token)
	if gate.Pending() {
		logger.Warn("first-run setup is pending: the startup wizard is open only to direct local-network clients; to run it through a proxy or from another network, open this link once",
			slog.String("path", manager.Network().NormalizedBaseURL()+"/deadfin/v1/setup?token="+token))
	}
	return gate, nil
}
