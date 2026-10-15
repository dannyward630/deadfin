// SPDX-License-Identifier: GPL-2.0-only

package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

// DeviceTokens is the device-token persistence seam required by Resolver.
type DeviceTokens interface {
	Token(context.Context, string) (store.Device, error)
	RefreshDeviceIfCurrent(context.Context, store.Device) (store.Device, bool, error)
}

// APIKeys is the API-key persistence seam required by Resolver.
type APIKeys interface {
	APIKeyByToken(context.Context, string) (store.APIKey, error)
}

// ResolverConfig supplies the persistence and server-identity dependencies for a
// Resolver. Legacy authorization controls only the upstream compatibility forms.
type ResolverConfig struct {
	Devices                   DeviceTokens
	APIKeys                   APIKeys
	Server                    config.ServerIdentity
	EnableLegacyAuthorization bool
	// LegacyAuthorizationEnabled is read for every resolved request when set.
	// EnableLegacyAuthorization remains the static fallback for direct callers.
	LegacyAuthorizationEnabled func() bool
}

// Resolver resolves request credentials to an authenticated identity.
type Resolver struct {
	devices                    DeviceTokens
	apiKeys                    APIKeys
	server                     config.ServerIdentity
	enableLegacyAuthorization  bool
	legacyAuthorizationEnabled func() bool
}

// NewResolver constructs a request credential resolver.
func NewResolver(config ResolverConfig) *Resolver {
	return &Resolver{
		devices:                    config.Devices,
		apiKeys:                    config.APIKeys,
		server:                     config.Server,
		enableLegacyAuthorization:  config.EnableLegacyAuthorization,
		legacyAuthorizationEnabled: config.LegacyAuthorizationEnabled,
	}
}

func (resolver *Resolver) legacyEnabled() bool {
	if resolver.legacyAuthorizationEnabled != nil {
		return resolver.legacyAuthorizationEnabled()
	}
	return resolver.enableLegacyAuthorization
}

// Credentials are the client facts carried by Jellyfin's authorization headers.
type Credentials struct {
	Token    string
	Client   string
	Device   string
	DeviceID string
	Version  string
}

// Identity is the resolved credential identity for one HTTP request.
type Identity struct {
	Authenticated bool
	APIKey        bool
	Administrator bool
	Token         string
	UserID        dto.UUID
	Client        string
	Device        string
	DeviceID      string
	Version       string
}

// Extract applies Jellyfin's authorization-token precedence to request.
func Extract(request *http.Request, enableLegacyAuthorization bool) Credentials {
	auth := authorizationDictionary(request, enableLegacyAuthorization)
	credentials := Credentials{}
	if auth != nil {
		credentials.DeviceID = auth["DeviceId"]
		credentials.Device = auth["Device"]
		credentials.Client = auth["Client"]
		credentials.Version = auth["Version"]
		credentials.Token = auth["Token"]
	}

	if enableLegacyAuthorization && credentials.Token == "" {
		credentials.Token = headerScalar(request.Header, "X-Emby-Token")
	}
	if enableLegacyAuthorization && credentials.Token == "" {
		credentials.Token = headerScalar(request.Header, "X-MediaBrowser-Token")
	}
	if credentials.Token == "" {
		credentials.Token = queryScalar(request.URL.RawQuery, "ApiKey")
	}
	if enableLegacyAuthorization && credentials.Token == "" {
		credentials.Token = queryScalar(request.URL.RawQuery, "api_key")
	}

	return credentials
}

func authorizationDictionary(request *http.Request, enableLegacyAuthorization bool) map[string]string {
	values := headerValues(request.Header, "Authorization")
	authorization := first(values)
	if enableLegacyAuthorization && authorization == "" && scalar(values) == "" {
		authorization = first(headerValues(request.Header, "X-Emby-Authorization"))
	}
	if authorization == "" {
		return nil
	}

	firstSpace := strings.IndexByte(authorization, ' ')
	if firstSpace < 0 {
		return nil
	}

	scheme := authorization[:firstSpace]
	if !strings.EqualFold(scheme, "MediaBrowser") && (!enableLegacyAuthorization || !strings.EqualFold(scheme, "Emby")) {
		return nil
	}

	return authorizationParts(authorization[firstSpace+1:])
}

// authorizationParts ports AuthorizationContext.GetParts. It intentionally keeps
// its case-sensitive keys and quoted-comma behavior because clients depend on it.
func authorizationParts(value string) map[string]string {
	parts := make(map[string]string)
	escaped := false
	start := 0
	key := ""

	for index := range len(value) {
		token := value[index]
		switch token {
		case '"', ',':
			escaped = (!escaped) == (token == '"')
			if token == ',' && !escaped {
				if start < index {
					parts[key] = urlDecode(value[start:index])
					key = ""
				}
				start = index + 1
			}
		case '=':
			if !escaped {
				key = strings.TrimSpace(value[start:index])
				start = index + 1
			}
		}
	}
	if start < len(value) {
		parts[key] = urlDecode(value[start:])
	}

	return parts
}

func urlDecode(value string) string {
	return decodeURLComponent(strings.Trim(value, "\""))
}

func decodeURLComponent(value string) string {
	firstEncoded := -1
	for index := range len(value) {
		if value[index] == '+' || (value[index] == '%' && index+2 < len(value) && validEscape(value[index+1], value[index+2])) {
			firstEncoded = index
			break
		}
	}
	if firstEncoded < 0 {
		return value
	}

	var decoded strings.Builder
	decoded.Grow(len(value))
	decoded.WriteString(value[:firstEncoded])
	for index := firstEncoded; index < len(value); {
		switch {
		case value[index] == '+':
			decoded.WriteByte(' ')
			index++
		case value[index] == '%' && index+2 < len(value) && validEscape(value[index+1], value[index+2]):
			decoded.WriteByte(hex(value[index+1])<<4 | hex(value[index+2]))
			index += 3
		default:
			decoded.WriteByte(value[index])
			index++
		}
	}
	value = decoded.String()
	if utf8.ValidString(value) {
		return value
	}
	return replaceInvalidUTF8(value)
}

func replaceInvalidUTF8(value string) string {
	var normalized strings.Builder
	normalized.Grow(len(value))
	for len(value) != 0 {
		rune, size := utf8.DecodeRuneInString(value)
		if rune == utf8.RuneError && size == 1 {
			normalized.WriteRune(utf8.RuneError)
			value = value[1:]
			continue
		}
		normalized.WriteString(value[:size])
		value = value[size:]
	}
	return normalized.String()
}

func validEscape(high, low byte) bool {
	return hex(high) != 0xff && hex(low) != 0xff
}

func hex(value byte) byte {
	switch {
	case value >= '0' && value <= '9':
		return value - '0'
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10
	default:
		return 0xff
	}
}

// queryURLDecode preserves invalid percent-encoded UTF-8 bytes as their raw
// escapes, unlike the WebUtility-compatible authorization dictionary decoder.
func queryURLDecode(value string) string {
	firstEncoded := -1
	for index := range len(value) {
		if value[index] == '+' || (value[index] == '%' && index+2 < len(value) && validEscape(value[index+1], value[index+2])) {
			firstEncoded = index
			break
		}
	}
	if firstEncoded < 0 {
		return value
	}

	var decoded strings.Builder
	decoded.Grow(len(value))
	decoded.WriteString(value[:firstEncoded])
	for index := firstEncoded; index < len(value); {
		switch {
		case value[index] == '+':
			decoded.WriteByte(' ')
			index++
		case value[index] == '%' && index+2 < len(value) && validEscape(value[index+1], value[index+2]):
			start := index
			for index+2 < len(value) && value[index] == '%' && validEscape(value[index+1], value[index+2]) {
				index += 3
			}
			appendQueryPercentRun(&decoded, value, start, index)
		default:
			decoded.WriteByte(value[index])
			index++
		}
	}
	return decoded.String()
}

func appendQueryPercentRun(decoded *strings.Builder, raw string, start, end int) {
	encoded := make([]byte, 0, (end-start)/3)
	for index := start; index < end; index += 3 {
		encoded = append(encoded, hex(raw[index+1])<<4|hex(raw[index+2]))
	}
	for index := 0; index < len(encoded); {
		rune, size := utf8.DecodeRune(encoded[index:])
		if rune == utf8.RuneError && size == 1 {
			rawStart := start + index*3
			decoded.WriteString(raw[rawStart : rawStart+3])
			index++
			continue
		}
		decoded.Write(encoded[index : index+size])
		index += size
	}
}

func headerValues(headers http.Header, name string) []string {
	values := headers.Values(name)
	if len(values) != 0 {
		return values
	}
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			return values
		}
	}
	return nil
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func headerScalar(headers http.Header, name string) string {
	return scalar(headerValues(headers, name))
}

func queryScalar(rawQuery, name string) string {
	firstValue := ""
	var values strings.Builder
	for rawQuery != "" {
		parameter, remainder, found := strings.Cut(rawQuery, "&")
		if found {
			rawQuery = remainder
		} else {
			rawQuery = ""
		}
		key, value, _ := strings.Cut(parameter, "=")
		if !strings.EqualFold(queryURLDecode(key), name) {
			continue
		}
		value = queryURLDecode(value)
		if value == "" {
			continue
		}
		if firstValue == "" {
			firstValue = value
			continue
		}
		if values.Len() == 0 {
			values.Grow(len(firstValue) + len(value) + 1)
			values.WriteString(firstValue)
		}
		values.WriteByte(',')
		values.WriteString(value)
	}
	if values.Len() == 0 {
		return firstValue
	}
	return values.String()
}

func scalar(values []string) string {
	firstValue := ""
	var result strings.Builder
	for _, value := range values {
		if value == "" {
			continue
		}
		if firstValue == "" {
			firstValue = value
			continue
		}
		if result.Len() == 0 {
			result.Grow(len(firstValue) + len(value) + 1)
			result.WriteString(firstValue)
		}
		result.WriteByte(',')
		result.WriteString(value)
	}
	if result.Len() == 0 {
		return firstValue
	}
	return result.String()
}

type requestCacheKey struct{}

type requestCache struct {
	once     sync.Once
	identity Identity
	err      error
}

// NewRequestCache installs a request-scoped resolver cache in ctx. If one is
// already present, it is retained so all request stages share its result.
func NewRequestCache(ctx context.Context) context.Context {
	if _, ok := ctx.Value(requestCacheKey{}).(*requestCache); ok {
		return ctx
	}
	return context.WithValue(ctx, requestCacheKey{}, &requestCache{})
}

// Resolve extracts and resolves request credentials. When NewRequestCache is
// installed, every call for that request shares the lookup and refresh result.
func (resolver *Resolver) Resolve(ctx context.Context, request *http.Request) (Identity, error) {
	if cache, ok := ctx.Value(requestCacheKey{}).(*requestCache); ok {
		cache.once.Do(func() {
			cache.identity, cache.err = resolver.resolve(ctx, request)
		})
		return cache.identity, cache.err
	}
	if cache, ok := request.Context().Value(requestCacheKey{}).(*requestCache); ok {
		cache.once.Do(func() {
			cache.identity, cache.err = resolver.resolve(ctx, request)
		})
		return cache.identity, cache.err
	}
	return resolver.resolve(ctx, request)
}

func (resolver *Resolver) resolve(ctx context.Context, request *http.Request) (Identity, error) {
	credentials := Extract(request, resolver.legacyEnabled())
	identity := Identity{
		Token:    credentials.Token,
		Client:   credentials.Client,
		Device:   credentials.Device,
		DeviceID: credentials.DeviceID,
		Version:  credentials.Version,
	}
	if strings.TrimSpace(identity.Token) == "" {
		return identity, nil
	}

	if resolver.devices != nil {
		device, err := resolver.devices.Token(ctx, identity.Token)
		switch {
		case err == nil:
			return resolver.resolveDevice(ctx, identity, device)
		case !errors.Is(err, store.ErrNotFound):
			return Identity{}, err
		}
	}

	if resolver.apiKeys == nil {
		return identity, nil
	}
	key, err := resolver.apiKeys.APIKeyByToken(ctx, identity.Token)
	switch {
	case err == nil:
		return Identity{
			Authenticated: true,
			APIKey:        true,
			Administrator: true,
			Token:         key.AccessToken,
			Client:        key.Name,
			Device:        fallback(identity.Device, resolver.server.ServerName),
			DeviceID:      fallback(identity.DeviceID, resolver.server.ID.String()),
			Version:       fallback(identity.Version, resolver.server.Version),
		}, nil
	case errors.Is(err, store.ErrNotFound):
		return identity, nil
	default:
		return Identity{}, err
	}
}

func fallback(value, defaultValue string) string {
	if strings.TrimSpace(value) == "" {
		return defaultValue
	}
	return value
}

func (resolver *Resolver) resolveDevice(ctx context.Context, identity Identity, device store.Device) (Identity, error) {
	identity.Authenticated = true
	identity.UserID = device.UserID
	if strings.TrimSpace(identity.Client) == "" {
		identity.Client = device.AppName
	}
	if strings.TrimSpace(identity.DeviceID) == "" {
		identity.DeviceID = device.DeviceID
	}

	allowTokenInfoUpdate := !containsChromecast(identity.Client)
	if strings.TrimSpace(identity.Device) == "" {
		identity.Device = device.DeviceName
	} else if allowTokenInfoUpdate && !strings.EqualFold(identity.Device, device.DeviceName) {
		device.DeviceName = identity.Device
	}
	if strings.TrimSpace(identity.Version) == "" {
		identity.Version = device.AppVersion
	} else if allowTokenInfoUpdate && !strings.EqualFold(identity.Version, device.AppVersion) {
		device.AppVersion = identity.Version
	}

	if _, _, err := resolver.devices.RefreshDeviceIfCurrent(ctx, device); err != nil && !errors.Is(err, store.ErrNotFound) {
		return Identity{}, err
	}
	return identity, nil
}

func containsChromecast(value string) bool {
	const chromecast = "chromecast"
	for start := range len(value) - len(chromecast) + 1 {
		if strings.EqualFold(value[start:start+len(chromecast)], chromecast) {
			return true
		}
	}
	return false
}
