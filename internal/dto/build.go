// SPDX-License-Identifier: GPL-2.0-only

package dto

// BuildInfo is the body of GET /deadfin/v1/build. No field is omitempty: BuildTime renders "" and Dirty renders false.
type BuildInfo struct {
	Component           string   `json:"Component"`
	Version             string   `json:"Version"`
	Channel             string   `json:"Channel"`
	Revision            string   `json:"Revision"`
	Dirty               bool     `json:"Dirty"`
	BuildTime           string   `json:"BuildTime"`
	CompatibilityTarget string   `json:"CompatibilityTarget"`
	APIVersion          int      `json:"ApiVersion"`
	Capabilities        []string `json:"Capabilities"`
}
