// SPDX-License-Identifier: GPL-2.0-only

package dto

// BrandingConfiguration is the branding subset returned to API clients.
type BrandingConfiguration struct {
	LoginDisclaimer     *string `json:"LoginDisclaimer,omitzero"`
	CustomCss           *string `json:"CustomCss,omitzero"`
	SplashscreenEnabled bool    `json:"SplashscreenEnabled"`
}
