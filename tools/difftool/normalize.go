// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func newNormalizer(rules []Rule) normalizer {
	var jitter time.Duration
	for _, rule := range rules {
		if rule.Action == "date_jitter" {
			jitter = time.Duration(rule.Seconds) * time.Second
		}
	}
	return normalizer{rules: rules, dateJitter: jitter}
}

func (n normalizer) normalize(response Response, itemIDs map[string]string) (normalizedResponse, error) {
	normalized := normalizedResponse{Status: response.Status, Headers: normalizeHeaders(response.Headers, n.rules)}
	body := response.Body
	if payload, ok := decodeJSON(body); ok {
		normalizedItemIDs := itemIDs
		for _, rule := range n.rules {
			if rule.Action == "item_ids" {
				normalizedItemIDs = discoverMediaSourceIDs(payload, itemIDs)
				break
			}
		}
		for _, rule := range n.rules {
			switch rule.Action {
			case "item_ids":
				payload = normalizeItemIDs(payload, normalizedItemIDs)
			case "server_id":
				payload = normalizeServerID(payload, true)
			case "auth_tokens":
				payload = normalizeAuthenticationTokens(payload)
			case "startup_user_name":
				payload = normalizeStartupUserName(payload)
			case "login_session_volatiles":
				payload = normalizeLoginSessionVolatiles(payload)
			case "entity_tags":
				payload = normalizeEntityTags(payload)
			case "item_created_dates":
				payload = normalizeItemCreatedDates(payload)
			case "user_data_keys":
				payload = normalizeItemUserDataKeys(payload)
			case "item_user_data_playback_timestamps":
				payload = normalizeItemUserDataPlaybackTimestamps(payload)
			case "user_view_ids":
				payload = normalizeUserViewIDs(payload)
			case "environment_fields":
				payload = normalizeEnvironmentFields(payload)
			case "full_system_runtime_capabilities":
				payload = normalizeFullSystemRuntimeCapabilities(payload)
			case "play_session_ids":
				payload = normalizePlaySessionIDs(payload)
			case "transcoding_url_tokens":
				if rule.Name == subtitleDeliveryURLItemIDsRuleName {
					updated, err := normalizeSubtitleDeliveryURLItemIDs(payload, normalizedItemIDs)
					if err != nil {
						return normalizedResponse{}, err
					}
					payload = updated
				} else {
					payload = normalizeTranscodingURLTokens(payload, normalizedItemIDs)
				}
			case "library_folder_ids":
				payload = normalizeLibraryFolderIDs(payload)
			case "library_options_defaults":
				payload = normalizeLibraryOptionsDefaults(payload)
			case "library_options_persisted":
				payload = normalizeLibraryOptionsPersisted(payload)
			case "quickconnect_volatiles":
				payload = normalizeQuickConnectVolatiles(payload)
			case "problem_details_trace":
				payload = normalizeProblemDetailsTrace(payload)
			case "external_id_infos":
				payload = normalizeExternalIdInfos(payload)
			case "remote_search_result_order":
				payload = normalizeRemoteSearchResultOrder(payload)
			}
		}
		normalized.Body = payload
		normalized.IsJSON = true
		return normalized, nil
	}
	text := string(body)
	for _, rule := range n.rules {
		if rule.Action == "playlist_replace" {
			text = regexp.MustCompile(rule.Pattern).ReplaceAllString(text, rule.Replacement)
		}
	}
	normalized.Body = text
	return normalized, nil
}

func normalizeHeaders(headers http.Header, rules []Rule) map[string][]string {
	drop := map[string]bool{}
	for _, rule := range rules {
		if rule.Action == "drop_header" {
			drop[strings.ToLower(rule.Header)] = true
		}
	}
	normalized := make(map[string][]string, len(headers))
	for name, values := range headers {
		name = strings.ToLower(name)
		if drop[name] {
			continue
		}
		copyValues := append([]string(nil), values...)
		sort.Strings(copyValues)
		normalized[name] = copyValues
	}
	return normalized
}

func decodeJSON(body []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return value, true
}

// discoverMediaSourceIDs extends an inventory-derived ID map with source IDs
// carried by item DTOs in this payload. A source path identifies the concrete
// version, while its enclosing item Type supplies the item kind. The input map
// is never mutated because it is retained by the replay host across responses.
func discoverMediaSourceIDs(value any, itemIDs map[string]string) map[string]string {
	var candidates map[string]string
	var ambiguous map[string]bool

	register := func(rawID, canonical string) {
		if _, known := itemIDs[rawID]; known {
			return
		}
		if candidates == nil {
			candidates = make(map[string]string)
		}
		if previous, seen := candidates[rawID]; seen {
			if previous != canonical {
				if ambiguous == nil {
					ambiguous = make(map[string]bool)
				}
				ambiguous[rawID] = true
			}
			return
		}
		candidates[rawID] = canonical
	}

	var discover func(any)
	discover = func(value any) {
		switch typed := value.(type) {
		case []any:
			for _, entry := range typed {
				discover(entry)
			}
		case map[string]any:
			if kind, ok := stringField(typed, "Type"); ok {
				for key, entry := range typed {
					if strings.EqualFold(key, "MediaSources") {
						registerMediaSourceIDs(entry, kind, register)
					}
				}
			}
			for _, entry := range typed {
				discover(entry)
			}
		}
	}
	discover(value)

	if len(candidates) == 0 {
		return itemIDs
	}
	resolved := itemIDs
	copied := false
	for rawID, canonical := range candidates {
		if ambiguous[rawID] {
			continue
		}
		if !copied {
			resolved = make(map[string]string, len(itemIDs)+len(candidates))
			for existingID, existingCanonical := range itemIDs {
				resolved[existingID] = existingCanonical
			}
			copied = true
		}
		resolved[rawID] = canonical
	}
	return resolved
}

func registerMediaSourceIDs(value any, kind string, register func(string, string)) {
	sources, ok := value.([]any)
	if !ok {
		return
	}
	for _, entry := range sources {
		source, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		rawID, hasID := stringField(source, "Id")
		path, hasPath := stringField(source, "Path")
		if hasID && hasPath {
			register(rawID, kind+":"+path)
		}
	}
}

func stringField(value map[string]any, name string) (string, bool) {
	var result string
	var found bool
	for key, entry := range value {
		if !strings.EqualFold(key, name) {
			continue
		}
		text, ok := entry.(string)
		if !ok || text == "" {
			return "", false
		}
		if found && result != text {
			return "", false
		}
		result = text
		found = true
	}
	return result, found
}

func normalizeItemIDs(value any, ids map[string]string) any {
	switch typed := value.(type) {
	case string:
		if canonical, ok := ids[typed]; ok {
			return "<item:" + canonical + ">"
		}
		return typed
	case []any:
		for index := range typed {
			typed[index] = normalizeItemIDs(typed[index], ids)
		}
		return typed
	case map[string]any:
		for key, entry := range typed {
			if isOpaqueIdentityField(key) {
				continue
			}
			typed[key] = normalizeItemIDs(entry, ids)
		}
		return typed
	default:
		return value
	}
}

func isOpaqueIdentityField(name string) bool {
	return strings.EqualFold(name, "ServerId") ||
		strings.EqualFold(name, "AccessToken") ||
		strings.EqualFold(name, "Token")
}

// normalizeServerID redacts a server identity only in DTO positions where it
// is volatile: the root public-system and login responses, and recognized
// item, user, and session DTOs. It recursively reaches nested DTOs, but does
// not infer meaning from a generic key in an arbitrary object (including
// dictionary values).
func normalizeServerID(value any, atRoot bool) any {
	switch typed := value.(type) {
	case []any:
		for index := range typed {
			typed[index] = normalizeServerID(typed[index], false)
		}
		return typed
	case map[string]any:
		switch {
		case atRoot && isPublicSystemInfo(typed):
			normalizePublicSystemServerID(typed)
		case atRoot && isLoginResponse(typed):
			normalizeItemDTOServerID(typed)
		case isItemDTO(typed), isLoginUserDTO(typed), isLoginSessionDTO(typed):
			normalizeItemDTOServerID(typed)
		}
		for key, entry := range typed {
			typed[key] = normalizeServerID(entry, false)
		}
		return typed
	default:
		return value
	}
}

func normalizePublicSystemServerID(value map[string]any) {
	for _, field := range []string{"Id", "id", "ServerId", "serverId"} {
		if _, ok := value[field].(string); ok {
			value[field] = "<server-id>"
		}
	}
}

func normalizeItemDTOServerID(value map[string]any) {
	for _, field := range []string{"ServerId", "serverId"} {
		if _, ok := value[field].(string); ok {
			value[field] = "<server-id>"
		}
	}
}

func isPublicSystemInfo(value map[string]any) bool {
	_, hasProductName := value["ProductName"].(string)
	if !hasProductName {
		_, hasProductName = value["productName"].(string)
	}
	_, hasVersion := value["Version"].(string)
	if !hasVersion {
		_, hasVersion = value["version"].(string)
	}
	return hasProductName && hasVersion
}

// normalizeAuthenticationTokens removes only the opaque bearer field at the
// root of an explicitly selected authentication response. It deliberately
// does not recurse or treat generic token-shaped fields as volatile.
func normalizeAuthenticationTokens(value any) any {
	payload, ok := value.(map[string]any)
	if !ok {
		return value
	}
	for _, field := range []string{"AccessToken", "accessToken"} {
		if _, ok := payload[field].(string); ok {
			payload[field] = "<auth-token>"
		}
	}
	return payload
}

// normalizeStartupUserName replaces only a direct root Name string in the
// explicitly selected initial StartupUser reads. It does not recurse, so
// nested DTOs and unrelated Name fields retain their semantic values.
func normalizeStartupUserName(value any) any {
	payload, ok := value.(map[string]any)
	if !ok {
		return value
	}
	for key, entry := range payload {
		if strings.EqualFold(key, "Name") {
			if _, ok := entry.(string); ok {
				payload[key] = "<startup-user-name>"
			}
		}
	}
	return payload
}

// normalizeLoginSessionVolatiles replaces independently minted authenticated
// user and session values only in the response positions emitted by the login
// corpus. It deliberately does not recurse: generic IDs, dates, endpoints,
// map keys, and nested DTOs outside these positions remain comparable.
func normalizeLoginSessionVolatiles(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		if isLoginUserDTO(typed) {
			normalizeLoginUserDTO(typed)
		}
		for key, entry := range typed {
			entry, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			switch {
			case strings.EqualFold(key, "User") && isLoginUserDTO(entry):
				normalizeLoginUserDTO(entry)
			case strings.EqualFold(key, "SessionInfo") && isLoginSessionDTO(entry):
				normalizeLoginSessionDTO(entry)
			}
		}
	case []any:
		for _, entry := range typed {
			dto, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			switch {
			case isLoginSessionDTO(dto):
				normalizeLoginSessionDTO(dto)
			case isLoginUserDTO(dto):
				normalizeLoginUserDTO(dto)
			}
		}
	}
	return value
}

func normalizeLoginUserDTO(user map[string]any) {
	for key, entry := range user {
		switch {
		case strings.EqualFold(key, "Id"):
			if _, ok := entry.(string); ok {
				user[key] = "<authenticated-user-id>"
			}
		case strings.EqualFold(key, "LastActivityDate"):
			if timestamp, ok := entry.(string); ok && isTimestamp(timestamp) {
				user[key] = "<authenticated-activity-date>"
			}
		case strings.EqualFold(key, "LastLoginDate"):
			if timestamp, ok := entry.(string); ok && isTimestamp(timestamp) {
				user[key] = "<authenticated-login-date>"
			}
		}
	}
}

func normalizeLoginSessionDTO(session map[string]any) {
	for key, entry := range session {
		switch {
		case strings.EqualFold(key, "Id"):
			if _, ok := entry.(string); ok {
				session[key] = "<authenticated-session-id>"
			}
		case strings.EqualFold(key, "UserId"):
			if _, ok := entry.(string); ok {
				session[key] = "<authenticated-user-id>"
			}
		case strings.EqualFold(key, "LastActivityDate"):
			if timestamp, ok := entry.(string); ok && isTimestamp(timestamp) {
				session[key] = "<authenticated-activity-date>"
			}
		case strings.EqualFold(key, "LastLoginDate"):
			if timestamp, ok := entry.(string); ok && isTimestamp(timestamp) {
				session[key] = "<authenticated-login-date>"
			}
		case strings.EqualFold(key, "RemoteEndPoint"):
			if _, ok := entry.(string); ok {
				session[key] = "<session-remote-endpoint>"
			}
		}
	}
}

func isLoginUserDTO(value map[string]any) bool {
	return hasStringField(value, "Id") && hasStringField(value, "Name") && hasObjectField(value, "Policy")
}

func isLoginSessionDTO(value map[string]any) bool {
	return hasStringField(value, "Id") &&
		hasStringField(value, "UserId") &&
		hasStringField(value, "Client") &&
		hasStringField(value, "DeviceId") &&
		hasStringField(value, "DeviceName")
}

func isLoginResponse(value map[string]any) bool {
	user, ok := value["User"].(map[string]any)
	if !ok {
		user, ok = value["user"].(map[string]any)
	}
	if !ok || !isLoginUserDTO(user) {
		return false
	}
	session, ok := value["SessionInfo"].(map[string]any)
	if !ok {
		session, ok = value["sessionInfo"].(map[string]any)
	}
	return ok && isLoginSessionDTO(session)
}

func hasStringField(value map[string]any, field string) bool {
	for key, entry := range value {
		if strings.EqualFold(key, field) {
			_, ok := entry.(string)
			return ok
		}
	}
	return false
}

func hasObjectField(value map[string]any, field string) bool {
	for key, entry := range value {
		if strings.EqualFold(key, field) {
			_, ok := entry.(map[string]any)
			return ok
		}
	}
	return false
}

func isTimestamp(value string) bool {
	_, valid := parseTime(value)
	return valid
}

func normalizeEntityTags(value any) any {
	switch typed := value.(type) {
	case []any:
		for index := range typed {
			typed[index] = normalizeEntityTags(typed[index])
		}
		return typed
	case map[string]any:
		for key, entry := range typed {
			switch {
			case strings.EqualFold(key, "ETag"):
				if _, ok := entry.(string); ok {
					typed[key] = "<entity-tag>"
				}
			case isImageTagField(key):
				typed[key] = normalizeImageTagValues(entry)
			case strings.EqualFold(key, "ImageBlurHashes"):
				typed[key] = normalizeImageBlurHashTagKeys(entry)
			default:
				typed[key] = normalizeEntityTags(entry)
			}
		}
		return typed
	default:
		return value
	}
}

func normalizeImageTagValues(value any) any {
	switch typed := value.(type) {
	case string:
		return "<entity-tag>"
	case []any:
		for index := range typed {
			typed[index] = normalizeImageTagValues(typed[index])
		}
		return typed
	case map[string]any:
		for key, entry := range typed {
			typed[key] = normalizeImageTagValues(entry)
		}
		return typed
	default:
		return value
	}
}

func isImageTagField(key string) bool {
	return strings.EqualFold(key, "ImageTags") ||
		strings.EqualFold(key, "BackdropImageTags") ||
		strings.EqualFold(key, "ParentBackdropImageTags")
}

// normalizeImageBlurHashTagKeys replaces only cache-tag keys in the inner
// image-type maps. Values remain blur hashes and outer image-type keys remain
// meaningful response structure.
func normalizeImageBlurHashTagKeys(value any) any {
	imageTypes, ok := value.(map[string]any)
	if !ok {
		return value
	}
	for imageType, tags := range imageTypes {
		imageTypes[imageType] = normalizeImageBlurHashTagMap(tags)
	}
	return imageTypes
}

type imageBlurHashTag struct {
	key     string
	sortKey string
	value   any
}

func normalizeImageBlurHashTagMap(value any) any {
	tags, ok := value.(map[string]any)
	if !ok {
		return value
	}
	entries := make([]imageBlurHashTag, 0, len(tags))
	for key, entry := range tags {
		encoded, _ := json.Marshal(entry)
		entries = append(entries, imageBlurHashTag{key: key, sortKey: string(encoded), value: entry})
	}
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].sortKey == entries[right].sortKey {
			return entries[left].key < entries[right].key
		}
		return entries[left].sortKey < entries[right].sortKey
	})
	normalized := make(map[string]any, len(tags))
	for index, entry := range entries {
		key := "<entity-tag>"
		if len(entries) > 1 {
			key = fmt.Sprintf("<entity-tag:%d>", index)
		}
		normalized[key] = entry.value
	}
	return normalized
}

// normalizeItemCreatedDates redacts scan-generated DateCreated timestamps
// only from item DTOs. It leaves missing, non-string, invalid, and non-item
// DateCreated fields untouched so their presence and type remain comparable.
func normalizeItemCreatedDates(value any) any {
	switch typed := value.(type) {
	case []any:
		for index := range typed {
			typed[index] = normalizeItemCreatedDates(typed[index])
		}
		return typed
	case map[string]any:
		if isItemDTO(typed) {
			for key, entry := range typed {
				if strings.EqualFold(key, "DateCreated") {
					if date, ok := entry.(string); ok {
						if _, valid := parseTime(date); valid {
							typed[key] = "<item-created-date>"
						}
					}
					continue
				}
				typed[key] = normalizeItemCreatedDates(entry)
			}
			return typed
		}
		for key, entry := range typed {
			typed[key] = normalizeItemCreatedDates(entry)
		}
		return typed
	default:
		return value
	}
}

func isItemDTO(value map[string]any) bool {
	var hasID, hasType bool
	for key, entry := range value {
		switch {
		case strings.EqualFold(key, "Id"):
			_, hasID = entry.(string)
		case strings.EqualFold(key, "Type"):
			_, hasType = entry.(string)
		}
	}
	return hasID && hasType
}

// normalizeItemUserDataKeys redacts only the persisted user-data row key in
// recognized item DTOs. ItemId and playback state remain structural response
// semantics and are deliberately left untouched.
func normalizeItemUserDataKeys(value any) any {
	switch typed := value.(type) {
	case []any:
		for index := range typed {
			typed[index] = normalizeItemUserDataKeys(typed[index])
		}
		return typed
	case map[string]any:
		if isItemDTO(typed) {
			for key, entry := range typed {
				if strings.EqualFold(key, "UserData") {
					if userData, ok := entry.(map[string]any); ok {
						normalizeItemUserDataKey(userData)
					}
					continue
				}
				typed[key] = normalizeItemUserDataKeys(entry)
			}
			return typed
		}
		for key, entry := range typed {
			typed[key] = normalizeItemUserDataKeys(entry)
		}
		return typed
	default:
		return value
	}
}

func normalizeItemUserDataKey(userData map[string]any) {
	for key, entry := range userData {
		if strings.EqualFold(key, "Key") {
			if _, ok := entry.(string); ok {
				userData[key] = "<user-data-key>"
			}
			return
		}
	}
}

// normalizedItemUserDataPlaybackTimestamp marks a LastPlayedDate normalized
// from a recognized item UserData field. Its string representation remains the
// readable tag emitted in artifacts, while its distinct Go type preserves the
// normalization provenance required by the bounded comparator.
type normalizedItemUserDataPlaybackTimestamp string

const (
	itemUserDataPlaybackTimestampTagPrefix = "<item-user-data-playback-timestamp:"
	itemUserDataPlaybackTimestampTagSuffix = ">"
	itemUserDataPlaybackTimestampSkew      = 2 * time.Second
)

// normalizeItemUserDataPlaybackTimestamps tags only valid LastPlayedDate
// timestamps directly in UserData maps of recognized item DTOs. The tag
// preserves the original value for the bounded comparator; malformed,
// non-string, missing, and out-of-scope values remain exact.
func normalizeItemUserDataPlaybackTimestamps(value any) any {
	switch typed := value.(type) {
	case []any:
		for index := range typed {
			typed[index] = normalizeItemUserDataPlaybackTimestamps(typed[index])
		}
		return typed
	case map[string]any:
		if isItemDTO(typed) {
			if entry, found := uniqueCaseInsensitiveField(typed, "UserData"); found {
				if userData, ok := entry.(map[string]any); ok {
					normalizeItemUserDataPlaybackTimestamp(userData)
				}
			}
		}
		for key, entry := range typed {
			typed[key] = normalizeItemUserDataPlaybackTimestamps(entry)
		}
		return typed
	default:
		return value
	}
}

func normalizeItemUserDataPlaybackTimestamp(userData map[string]any) {
	lastPlayedDate, found := uniqueCaseInsensitiveField(userData, "LastPlayedDate")
	date, ok := lastPlayedDate.(string)
	if !found || !ok {
		return
	}
	if _, valid := parseTime(date); !valid {
		return
	}
	for key := range userData {
		if strings.EqualFold(key, "LastPlayedDate") {
			userData[key] = normalizedItemUserDataPlaybackTimestamp(itemUserDataPlaybackTimestampTagPrefix + date + itemUserDataPlaybackTimestampTagSuffix)
			return
		}
	}
}

// normalizeUserViewIDs replaces only library-view identities in the two
// explicitly selected response roots: a UserViews QueryResult Items array or a
// GroupingOptions root array. It does not walk nested DTOs or arbitrary maps.
func normalizeUserViewIDs(value any) any {
	switch payload := value.(type) {
	case map[string]any:
		itemsValue, found := uniqueCaseInsensitiveField(payload, "Items")
		if !found {
			return value
		}
		items, ok := itemsValue.([]any)
		if !ok {
			return value
		}
		views, ok := collectUserViewIdentities(items)
		if !ok {
			return value
		}
		normalizeUserViewIdentities(views)
		normalizeUserViewChildCounts(views)
		normalizeUserViewsParentID(views)
	case []any:
		views, ok := collectUserViewIdentities(payload)
		if !ok {
			return value
		}
		normalizeUserViewIdentities(views)
		normalizeUserViewChildCounts(views)
	}
	return value
}

type userViewIdentity struct {
	view  map[string]any
	token string
}

func collectUserViewIdentities(entries []any) ([]userViewIdentity, bool) {
	views := make([]userViewIdentity, 0, len(entries))
	for _, entry := range entries {
		view, ok := entry.(map[string]any)
		if !ok {
			return nil, false
		}
		token, ok := userViewIdentityToken(view)
		if !ok {
			return nil, false
		}
		views = append(views, userViewIdentity{view: view, token: token})
	}
	return views, true
}

func normalizeUserViewIdentities(views []userViewIdentity) {
	for _, view := range views {
		normalizeUserViewIdentity(view.view, view.token)
	}
}

const userViewChildCountToken = "<user-view-child-count>"

// normalizeUserViewChildCounts accepts only direct, decoded JSON integer
// ChildCount fields in the volatile range emitted by DtoService for UserViews.
// Missing fields and every non-integer or out-of-range representation remain
// exact, so comparison still exposes structural and semantic mismatches.
func normalizeUserViewChildCounts(views []userViewIdentity) {
	for _, view := range views {
		childCount, found := uniqueCaseInsensitiveField(view.view, "ChildCount")
		if !found || !isVolatileUserViewChildCount(childCount) {
			continue
		}
		for key := range view.view {
			if strings.EqualFold(key, "ChildCount") {
				view.view[key] = userViewChildCountToken
				break
			}
		}
	}
}

func isVolatileUserViewChildCount(value any) bool {
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	count, err := number.Int64()
	return err == nil && count >= 1 && count <= 9
}

func normalizeUserViewsParentID(views []userViewIdentity) {
	if len(views) == 0 {
		return
	}
	parentID, found := uniqueStringCaseInsensitiveField(views[0].view, "ParentId")
	if !found {
		return
	}
	for _, view := range views[1:] {
		candidate, found := uniqueStringCaseInsensitiveField(view.view, "ParentId")
		if !found || candidate != parentID {
			return
		}
	}
	for _, view := range views {
		replaceUniqueStringCaseInsensitiveField(view.view, "ParentId", "<user-views-parent-id>")
	}
}

func normalizeUserViewIdentity(view map[string]any, token string) {
	replaceUniqueStringCaseInsensitiveField(view, "Id", token)
	normalizeUserViewPath(view, token)
	replaceUniqueStringCaseInsensitiveField(view, "DisplayPreferencesId", token)
	userData, found := uniqueCaseInsensitiveField(view, "UserData")
	if !found {
		return
	}
	data, ok := userData.(map[string]any)
	if !ok {
		return
	}
	replaceUniqueStringCaseInsensitiveField(data, "ItemId", token)
}

// normalizeUserViewPath replaces only a direct library-view directory below a
// configured server's state root. It deliberately does not walk nested DTOs,
// accept paths from arbitrary roots, or infer paths missing from either side.
func normalizeUserViewPath(view map[string]any, token string) {
	name, found := uniqueStringCaseInsensitiveField(view, "Name")
	if !found {
		return
	}
	value, found := uniqueStringCaseInsensitiveField(view, "Path")
	if !found || !isDirectUserViewStatePath(value, name) {
		return
	}
	replaceUniqueStringCaseInsensitiveField(view, "Path", token)
}

func isDirectUserViewStatePath(value, name string) bool {
	for _, root := range []string{"/config/data/root/default/", "/state/root/default/"} {
		if !strings.HasPrefix(value, root) {
			continue
		}
		encodedName := strings.TrimPrefix(value, root)
		if encodedName == "" || strings.Contains(encodedName, "/") {
			return false
		}
		decodedName, err := url.PathUnescape(encodedName)
		return err == nil && decodedName == name
	}
	return false
}

func userViewIdentityToken(view map[string]any) (string, bool) {
	name, found := uniqueStringCaseInsensitiveField(view, "Name")
	if !found {
		return "", false
	}
	identity := strings.ToLower(name)
	if collectionType, found := uniqueStringCaseInsensitiveField(view, "CollectionType"); found {
		identity += "|" + strings.ToLower(collectionType)
	}
	return "<user-view-id:" + identity + ">", true
}

func uniqueCaseInsensitiveField(value map[string]any, name string) (any, bool) {
	var result any
	found := false
	for key, entry := range value {
		if !strings.EqualFold(key, name) {
			continue
		}
		if found {
			return nil, false
		}
		result = entry
		found = true
	}
	return result, found
}

func uniqueStringCaseInsensitiveField(value map[string]any, name string) (string, bool) {
	entry, found := uniqueCaseInsensitiveField(value, name)
	text, ok := entry.(string)
	return text, found && ok && text != ""
}

func replaceUniqueStringCaseInsensitiveField(value map[string]any, name, replacement string) {
	var field string
	for key, entry := range value {
		if !strings.EqualFold(key, name) {
			continue
		}
		if field != "" {
			return
		}
		if _, ok := entry.(string); !ok {
			return
		}
		field = key
	}
	if field != "" {
		value[field] = replacement
	}
}

// normalizeFullSystemRuntimeCapabilities removes only host-runtime capability
// fields from the selected full /System/Info response. It examines the root
// object once: nested objects, arrays, and dictionary keys remain exact.
func normalizeFullSystemRuntimeCapabilities(value any) any {
	payload, ok := value.(map[string]any)
	if !ok {
		return value
	}
	for key := range payload {
		switch {
		case strings.EqualFold(key, "CanSelfRestart"),
			strings.EqualFold(key, "CanSelfUpdate"),
			strings.EqualFold(key, "SupportsLibraryMonitor"),
			strings.EqualFold(key, "SystemArchitecture"):
			delete(payload, key)
		}
	}
	return payload
}

func normalizeEnvironmentFields(value any) any {
	switch typed := value.(type) {
	case []any:
		for index := range typed {
			typed[index] = normalizeEnvironmentFields(typed[index])
		}
		return typed
	case map[string]any:
		for key, entry := range typed {
			if sentinel := environmentFieldSentinel(key); sentinel != "" {
				if _, isString := entry.(string); isString {
					typed[key] = sentinel
				}
				continue
			}
			typed[key] = normalizeEnvironmentFields(entry)
		}
		return typed
	default:
		return value
	}
}

func environmentFieldSentinel(field string) string {
	switch {
	case strings.EqualFold(field, "LocalAddress"):
		return "<environment-local-address>"
	case strings.EqualFold(field, "ServerName"):
		return "<environment-server-name>"
	case strings.EqualFold(field, "ProgramDataPath"):
		return "<environment-program-data-path>"
	case strings.EqualFold(field, "WebPath"):
		return "<environment-web-path>"
	case strings.EqualFold(field, "ItemsByNamePath"):
		return "<environment-items-by-name-path>"
	case strings.EqualFold(field, "CachePath"):
		return "<environment-cache-path>"
	case strings.EqualFold(field, "LogPath"):
		return "<environment-log-path>"
	case strings.EqualFold(field, "InternalMetadataPath"):
		return "<environment-internal-metadata-path>"
	case strings.EqualFold(field, "TranscodingTempPath"):
		return "<environment-transcoding-temp-path>"
	default:
		return ""
	}
}

var transcodingURLAPIKeyValue = regexp.MustCompile(`(?i)([?&]ApiKey=)[^&#[:space:]]*`)

var subtitleDeliveryURLAPIKeyValue = regexp.MustCompile(`(?i)([?&]ApiKey=)[^&#[:space:]]+`)

var transcodingURLPathItemID = regexp.MustCompile(`(?i)(/videos/)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{32})(/)`)

var transcodingURLMediaSourceItemID = regexp.MustCompile(`(?i)([?&]MediaSourceId=)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{32})([&#]|$)`)

var transcodingURLTagValue = regexp.MustCompile(`(?i)([?&]Tag=)([^&#[:space:]]+)([&#]|$)`)

func normalizePlaySessionIDs(value any) any {
	switch typed := value.(type) {
	case []any:
		for index := range typed {
			typed[index] = normalizePlaySessionIDs(typed[index])
		}
		return typed
	case map[string]any:
		for key, entry := range typed {
			if strings.EqualFold(key, "PlaySessionId") {
				if _, isString := entry.(string); isString {
					typed[key] = "<play-session-id>"
				}
				continue
			}
			typed[key] = normalizePlaySessionIDs(entry)
		}
		return typed
	case string:
		return normalizePlaySessionIDURL(typed)
	default:
		return value
	}
}

func normalizePlaySessionIDURL(value string) string {
	queryStart, queryEnd, ok := playSessionQueryBounds(value)
	if !ok {
		return value
	}

	normalizedQuery, changed := normalizePlaySessionQuery(value[queryStart:queryEnd])
	if !changed {
		return value
	}
	return value[:queryStart] + normalizedQuery + value[queryEnd:]
}

func playSessionQueryBounds(value string) (int, int, bool) {
	parsed, err := url.Parse(value)
	if err == nil && parsed.RawQuery != "" {
		if _, err := url.ParseQuery(parsed.RawQuery); err != nil {
			return 0, 0, false
		}
		queryStart := strings.IndexByte(value, '?') + 1
		queryEnd := queryStart + len(parsed.RawQuery)
		if queryStart > 0 && queryEnd <= len(value) && value[queryStart:queryEnd] == parsed.RawQuery {
			return queryStart, queryEnd, true
		}
	}

	if value == "" || strings.ContainsAny(value, "?# \t\r\n") {
		return 0, 0, false
	}
	firstParameter, _, _ := strings.Cut(value, "&")
	if !strings.Contains(firstParameter, "=") {
		return 0, 0, false
	}
	if _, err := url.ParseQuery(value); err != nil {
		return 0, 0, false
	}
	return 0, len(value), true
}

func normalizePlaySessionQuery(query string) (string, bool) {
	var normalized strings.Builder
	changed := false
	written := 0
	for parameterStart := 0; parameterStart < len(query); {
		parameterEnd := strings.IndexByte(query[parameterStart:], '&')
		if parameterEnd < 0 {
			parameterEnd = len(query)
		} else {
			parameterEnd += parameterStart
		}
		parameter := query[parameterStart:parameterEnd]
		name, _, hasValue := strings.Cut(parameter, "=")
		decodedName, err := url.QueryUnescape(name)
		if hasValue && err == nil && strings.EqualFold(decodedName, "PlaySessionId") {
			if !changed {
				normalized.Grow(len(query))
			}
			normalized.WriteString(query[written : parameterStart+len(name)+1])
			normalized.WriteString("<play-session-id>")
			written = parameterEnd
			changed = true
		}
		if parameterEnd == len(query) {
			break
		}
		parameterStart = parameterEnd + 1
	}
	if !changed {
		return query, false
	}
	normalized.WriteString(query[written:])
	return normalized.String(), true
}

const subtitleDeliveryURLItemIDsRuleName = "subtitle-delivery-url-item-ids"

// normalizeSubtitleDeliveryURLItemIDs rewrites only the mapped item and media
// source UUID path components emitted by the subtitle stream delivery endpoint.
// It is deliberately rooted at a PlaybackInfo response so generic URL fields,
// item DTO MediaSources, and non-subtitle streams remain exact.
func normalizeSubtitleDeliveryURLItemIDs(value any, itemIDs map[string]string) (any, error) {
	payload, ok := value.(map[string]any)
	if !ok || !isPlaybackInfoPayload(payload) {
		return value, nil
	}
	for key, entry := range payload {
		if !strings.EqualFold(key, "MediaSources") {
			continue
		}
		sources, ok := entry.([]any)
		if !ok {
			continue
		}
		if err := normalizeSubtitleDeliveryURLMediaSources(sources, itemIDs); err != nil {
			return value, err
		}
	}
	return value, nil
}

func isPlaybackInfoPayload(value map[string]any) bool {
	var hasMediaSources, hasPlaybackField bool
	for key, entry := range value {
		switch {
		case strings.EqualFold(key, "MediaSources"):
			_, hasMediaSources = entry.([]any)
		case strings.EqualFold(key, "PlaySessionId"):
			_, hasPlaybackField = entry.(string)
		case strings.EqualFold(key, "ErrorCode"):
			_, hasPlaybackField = entry.(string)
		}
	}
	return hasMediaSources && hasPlaybackField
}

func normalizeSubtitleDeliveryURLMediaSources(sources []any, itemIDs map[string]string) error {
	for _, entry := range sources {
		source, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		for key, streamsValue := range source {
			if !strings.EqualFold(key, "MediaStreams") {
				continue
			}
			streams, ok := streamsValue.([]any)
			if !ok {
				continue
			}
			for _, streamValue := range streams {
				stream, ok := streamValue.(map[string]any)
				if !ok {
					continue
				}
				streamType, subtitle := stringField(stream, "Type")
				if !subtitle || !strings.EqualFold(streamType, "Subtitle") {
					continue
				}
				for deliveryKey, deliveryValue := range stream {
					if !strings.EqualFold(deliveryKey, "DeliveryUrl") {
						continue
					}
					deliveryURL, ok := deliveryValue.(string)
					if !ok {
						continue
					}
					normalized, err := normalizeSubtitleDeliveryURL(deliveryURL, itemIDs)
					if err != nil {
						return err
					}
					stream[deliveryKey] = normalized
				}
			}
		}
	}
	return nil
}

func normalizeSubtitleDeliveryURL(value string, itemIDs map[string]string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("malformed subtitle DeliveryUrl")
	}
	if parsed.RawQuery != "" {
		if _, err := url.ParseQuery(parsed.RawQuery); err != nil {
			return "", fmt.Errorf("malformed subtitle DeliveryUrl query")
		}
	}
	// Escaped paths are not the stock UUID path form. Leaving them exact avoids
	// decoding and re-encoding unrelated path bytes.
	if parsed.RawPath != "" {
		return value, nil
	}
	rawPath := parsed.EscapedPath()
	pathParts := strings.Split(rawPath, "/")
	if len(pathParts) < 5 || pathParts[0] != "" || !strings.EqualFold(pathParts[1], "Videos") || !strings.EqualFold(pathParts[4], "Subtitles") {
		return value, nil
	}
	if len(pathParts) < 6 || pathParts[2] == "" || pathParts[3] == "" || pathParts[5] == "" {
		return "", fmt.Errorf("malformed subtitle DeliveryUrl path")
	}
	itemCanonical, ok := mappedSubtitleDeliveryUUID(pathParts[2], itemIDs)
	if !ok {
		return "", fmt.Errorf("unresolved subtitle DeliveryUrl item ID")
	}
	sourceCanonical, ok := mappedSubtitleDeliveryUUID(pathParts[3], itemIDs)
	if !ok {
		return "", fmt.Errorf("unresolved subtitle DeliveryUrl media-source ID")
	}
	normalized := normalizeSubtitleDeliveryURLAPIKey(value, parsed)
	pathStart := strings.Index(normalized, rawPath)
	if pathStart < 0 {
		return "", fmt.Errorf("locate subtitle DeliveryUrl path")
	}
	pathParts[2] = "<item:" + itemCanonical + ">"
	pathParts[3] = "<item:" + sourceCanonical + ">"
	return normalized[:pathStart] + strings.Join(pathParts, "/") + normalized[pathStart+len(rawPath):], nil
}

// normalizeSubtitleDeliveryURLAPIKey replaces only a nonempty ApiKey value in
// the validated raw query. It leaves every delimiter, parameter order, spelling,
// and unrelated value byte intact.
func normalizeSubtitleDeliveryURLAPIKey(value string, parsed *url.URL) string {
	if parsed.RawQuery == "" {
		return value
	}
	queryStart := strings.IndexByte(value, '?') + 1
	queryEnd := queryStart + len(parsed.RawQuery)
	if queryStart == 0 || queryEnd > len(value) || value[queryStart:queryEnd] != parsed.RawQuery {
		return value
	}
	query := "?" + value[queryStart:queryEnd]
	normalized := subtitleDeliveryURLAPIKeyValue.ReplaceAllString(query, `${1}<transcoding-url-token>`)
	if normalized == query {
		return value
	}
	return value[:queryStart] + normalized[1:] + value[queryEnd:]
}

func mappedSubtitleDeliveryUUID(value string, itemIDs map[string]string) (string, bool) {
	if !isUUIDToken(value) {
		return "", false
	}
	return mappedItemID(value, itemIDs)
}

func isUUIDToken(value string) bool {
	if len(value) == 36 {
		for index := range value {
			if index == 8 || index == 13 || index == 18 || index == 23 {
				if value[index] != '-' {
					return false
				}
				continue
			}
			if !isHexDigit(value[index]) {
				return false
			}
		}
		return true
	}
	if len(value) != 32 {
		return false
	}
	for index := range value {
		if !isHexDigit(value[index]) {
			return false
		}
	}
	return true
}

func isHexDigit(value byte) bool {
	return '0' <= value && value <= '9' || 'a' <= value && value <= 'f' || 'A' <= value && value <= 'F'
}

func normalizeTranscodingURLTokens(value any, itemIDs map[string]string) any {
	switch typed := value.(type) {
	case []any:
		for index := range typed {
			typed[index] = normalizeTranscodingURLTokens(typed[index], itemIDs)
		}
		return typed
	case map[string]any:
		for key, entry := range typed {
			if strings.EqualFold(key, "TranscodingUrl") {
				if url, ok := entry.(string); ok {
					url = normalizeTranscodingURLItemIDs(url, itemIDs)
					url = transcodingURLTagValue.ReplaceAllString(url, `${1}<entity-tag>${3}`)
					typed[key] = transcodingURLAPIKeyValue.ReplaceAllString(url, `${1}<transcoding-url-token>`)
				}
				continue
			}
			typed[key] = normalizeTranscodingURLTokens(entry, itemIDs)
		}
		return typed
	default:
		return value
	}
}

func normalizeTranscodingURLItemIDs(url string, itemIDs map[string]string) string {
	url = transcodingURLPathItemID.ReplaceAllStringFunc(url, func(match string) string {
		const pathPrefixLength = len("/videos/")
		id := match[pathPrefixLength : len(match)-1]
		canonical, ok := mappedItemID(id, itemIDs)
		if !ok {
			return match
		}
		return match[:pathPrefixLength] + "<item:" + canonical + ">" + match[len(match)-1:]
	})
	return transcodingURLMediaSourceItemID.ReplaceAllStringFunc(url, func(match string) string {
		valueStart := strings.IndexByte(match, '=') + 1
		valueEnd := len(match)
		if last := match[valueEnd-1]; last == '&' || last == '#' {
			valueEnd--
		}
		canonical, ok := mappedItemID(match[valueStart:valueEnd], itemIDs)
		if !ok {
			return match
		}
		return match[:valueStart] + "<item:" + canonical + ">" + match[valueEnd:]
	})
}

func mappedItemID(id string, itemIDs map[string]string) (string, bool) {
	if canonical, ok := itemIDs[id]; ok {
		return canonical, true
	}
	undashed := strings.ReplaceAll(id, "-", "")
	if canonical, ok := itemIDs[undashed]; ok {
		return canonical, true
	}
	dashed := undashed[:8] + "-" + undashed[8:12] + "-" + undashed[12:16] + "-" + undashed[16:20] + "-" + undashed[20:]
	canonical, ok := itemIDs[dashed]
	return canonical, ok
}

// normalizedComparator preserves the timestamp chronology accumulated while a
// scenario is replayed. It is intentionally stateful: fresh construction is
// the scenario boundary, while equalNormalized provides the one-response
// convenience used by focused unit tests.
type normalizedComparator struct {
	left             []playbackTimestampObservation
	right            []playbackTimestampObservation
	responseSequence uint64
}

func newNormalizedComparator() *normalizedComparator {
	return &normalizedComparator{}
}

func (c *normalizedComparator) Compare(left, right normalizedResponse, jitter time.Duration) (bool, string) {
	if left.Status != right.Status {
		return false, fmt.Sprintf("status: left=%d right=%d", left.Status, right.Status)
	}
	if !equalHeaders(left.Headers, right.Headers) {
		return false, "headers differ"
	}
	if left.IsJSON != right.IsJSON {
		return false, "response body type differs (JSON versus non-JSON)"
	}
	if !left.IsJSON {
		if left.Body != right.Body {
			return false, "response text differs"
		}
		return true, ""
	}
	if path, equal := equalJSON(left.Body, right.Body, jitter, "$"); !equal {
		return false, "JSON differs at " + path
	}
	sequence := c.responseSequence
	c.responseSequence++
	c.left = append(c.left, collectNormalizedItemUserDataPlaybackTimestampObservations(left.Body, sequence, "$")...)
	c.right = append(c.right, collectNormalizedItemUserDataPlaybackTimestampObservations(right.Body, sequence, "$")...)
	if path, equal := equalPlaybackTimestampChronology(c.left, c.right); !equal {
		return false, "playback timestamp chronology differs at " + path
	}
	return true, ""
}

func equalNormalized(left, right normalizedResponse, jitter time.Duration) (bool, string) {
	return newNormalizedComparator().Compare(left, right, jitter)
}

func equalHeaders(left, right map[string][]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValues := range left {
		rightValues, ok := right[key]
		if !ok || len(leftValues) != len(rightValues) {
			return false
		}
		for index := range leftValues {
			if leftValues[index] != rightValues[index] {
				return false
			}
		}
	}
	return true
}

// playbackTimestampObservation identity is the numeric replay sequence plus
// structural path. Numeric sequence ordering avoids the "10" before "2"
// lexicographic trap once a scenario has more than nine responses.
type playbackTimestampObservation struct {
	sequence uint64
	path     string
	time     time.Time
}

func collectNormalizedItemUserDataPlaybackTimestampObservations(value any, sequence uint64, path string) []playbackTimestampObservation {
	observations := make([]playbackTimestampObservation, 0)
	appendNormalizedItemUserDataPlaybackTimestampObservations(&observations, value, sequence, path)
	return observations
}

func appendNormalizedItemUserDataPlaybackTimestampObservations(observations *[]playbackTimestampObservation, value any, sequence uint64, path string) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			appendNormalizedItemUserDataPlaybackTimestampObservations(observations, typed[key], sequence, path+"."+key)
		}
	case []any:
		for index := range typed {
			appendNormalizedItemUserDataPlaybackTimestampObservations(observations, typed[index], sequence, path+"["+strconv.Itoa(index)+"]")
		}
	case normalizedItemUserDataPlaybackTimestamp:
		timestamp, ok := parseNormalizedItemUserDataPlaybackTimestamp(typed)
		if !ok {
			// Malformed internal values remain subject to exact comparison below.
			return
		}
		*observations = append(*observations, playbackTimestampObservation{sequence: sequence, path: path, time: timestamp})
	}
}

func equalPlaybackTimestampChronology(left, right []playbackTimestampObservation) (string, bool) {
	if len(left) != len(right) {
		return "$", false
	}
	sortPlaybackTimestampObservations(left)
	sortPlaybackTimestampObservations(right)
	for index := range left {
		if left[index].sequence != right[index].sequence || left[index].path != right[index].path {
			return left[index].path, false
		}
	}
	return "$", true
}

func sortPlaybackTimestampObservations(observations []playbackTimestampObservation) {
	sort.Slice(observations, func(left, right int) bool {
		if observations[left].time.Equal(observations[right].time) {
			if observations[left].sequence == observations[right].sequence {
				return observations[left].path < observations[right].path
			}
			return observations[left].sequence < observations[right].sequence
		}
		return observations[left].time.Before(observations[right].time)
	})
}

// equalNormalizedItemUserDataPlaybackTimestamps compares only normalized
// timestamp values. Ordinary strings, including readable tag-shaped strings,
// stay on the exact string comparison path.
func equalNormalizedItemUserDataPlaybackTimestamps(left, right normalizedItemUserDataPlaybackTimestamp) bool {
	if left == right {
		return true
	}
	leftTime, leftOK := parseNormalizedItemUserDataPlaybackTimestamp(left)
	rightTime, rightOK := parseNormalizedItemUserDataPlaybackTimestamp(right)
	if !leftOK || !rightOK {
		return false
	}
	if leftTime.Before(rightTime) {
		return rightTime.Sub(leftTime) <= itemUserDataPlaybackTimestampSkew
	}
	return leftTime.Sub(rightTime) <= itemUserDataPlaybackTimestampSkew
}

func parseNormalizedItemUserDataPlaybackTimestamp(value normalizedItemUserDataPlaybackTimestamp) (time.Time, bool) {
	text := string(value)
	if !strings.HasPrefix(text, itemUserDataPlaybackTimestampTagPrefix) || !strings.HasSuffix(text, itemUserDataPlaybackTimestampTagSuffix) {
		return time.Time{}, false
	}
	original := strings.TrimSuffix(strings.TrimPrefix(text, itemUserDataPlaybackTimestampTagPrefix), itemUserDataPlaybackTimestampTagSuffix)
	return parseTime(original)
}

func equalJSON(left, right any, jitter time.Duration, path string) (string, bool) {
	switch leftValue := left.(type) {
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		if !ok || len(leftValue) != len(rightValue) {
			return path, false
		}
		keys := make([]string, 0, len(leftValue))
		for key := range leftValue {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			rightEntry, ok := rightValue[key]
			if !ok {
				return path + "." + key, false
			}
			if childPath, equal := equalJSON(leftValue[key], rightEntry, jitter, path+"."+key); !equal {
				return childPath, false
			}
		}
		return path, true
	case []any:
		rightValue, ok := right.([]any)
		if !ok || len(leftValue) != len(rightValue) {
			return path, false
		}
		for index := range leftValue {
			if childPath, equal := equalJSON(leftValue[index], rightValue[index], jitter, path+"["+strconv.Itoa(index)+"]"); !equal {
				return childPath, false
			}
		}
		return path, true
	case normalizedItemUserDataPlaybackTimestamp:
		rightValue, ok := right.(normalizedItemUserDataPlaybackTimestamp)
		return path, ok && equalNormalizedItemUserDataPlaybackTimestamps(leftValue, rightValue)
	case string:
		rightValue, ok := right.(string)
		if !ok {
			return path, false
		}
		if leftValue == rightValue {
			return path, true
		}
		if jitter > 0 && isScanRefreshTimestampPath(path) {
			leftTime, leftOK := parseTime(leftValue)
			rightTime, rightOK := parseTime(rightValue)
			if leftOK && rightOK && absDuration(leftTime.Sub(rightTime)) <= jitter {
				return path, true
			}
		}
		return path, false
	case json.Number:
		rightValue, ok := right.(json.Number)
		return path, ok && leftValue == rightValue
	case bool:
		rightValue, ok := right.(bool)
		return path, ok && leftValue == rightValue
	case nil:
		return path, right == nil
	default:
		return path, false
	}
}

func isScanRefreshTimestampPath(path string) bool {
	const scanRefreshTimestampField = "DateLastRefreshed"
	return strings.HasSuffix(path, "."+scanRefreshTimestampField)
}

func parseTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

// normalizeLibraryFolderIDs replaces a string ItemId field in each element of
// the VirtualFolders response array with a stable token. The CollectionFolder
// identity is derived from each host's library root path and is a documented
// host-local scalar that is not behavior under comparison. Only a string
// ItemId is normalized; a non-string ItemId (wrong type) is left intact so the
// type mismatch remains comparable. Array order is preserved so response-order
// differences remain comparable.
func normalizeLibraryFolderIDs(value any) any {
	entries, ok := value.([]any)
	if !ok {
		return value
	}
	for _, entry := range entries {
		folder, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		for key, field := range folder {
			if strings.EqualFold(key, "ItemId") {
				if _, isString := field.(string); isString {
					folder[key] = "<library-folder-id>"
				}
			}
		}
	}
	return value
}

// normalizeLibraryOptionsDefaults replaces a JSON-object LibraryOptions field
// in each element of the VirtualFolders response array with a stable token.
// Default LibraryOptions values are populated by each server's compatibility
// defaults and are not part of the management-route contract. Only a JSON
// object (map[string]any) LibraryOptions is normalized; a non-object
// LibraryOptions (wrong type) is left intact so the type mismatch remains
// comparable. This action masks default options by explicit selection; it is
// the corpus that scopes it to default-readback steps, so persisted-option
// changes stay visible when the corpus omits it.
func normalizeLibraryOptionsDefaults(value any) any {
	entries, ok := value.([]any)
	if !ok {
		return value
	}
	for _, entry := range entries {
		folder, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		for key, field := range folder {
			if strings.EqualFold(key, "LibraryOptions") {
				if _, isObject := field.(map[string]any); isObject {
					folder[key] = "<library-options>"
				}
			}
		}
	}
	return value
}

// normalizeLibraryOptionsPersisted masks host-local default LibraryOptions
// fields while preserving the explicitly persisted option values. Each
// JSON-object LibraryOptions in the VirtualFolders response array has its
// default fields replaced with a stable token; the persisted fields
// (EnableInternetProviders, SaveLocalMetadata) keep their actual values so a
// Deadfin that does not persist the same values as stock is caught. A
// non-object LibraryOptions (wrong type) is left intact so the type mismatch
// remains comparable. This is the persisted-options readback counterpart to
// normalizeLibraryOptionsDefaults: it masks defaults while keeping persisted
// fields, where the defaults action masks the whole object.
func normalizeLibraryOptionsPersisted(value any) any {
	entries, ok := value.([]any)
	if !ok {
		return value
	}
	for _, entry := range entries {
		folder, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		for key, field := range folder {
			if !strings.EqualFold(key, "LibraryOptions") {
				continue
			}
			options, ok := field.(map[string]any)
			if !ok {
				continue
			}
			masked := make(map[string]any, 3)
			hasDefaults := false
			for optKey, optValue := range options {
				if isPersistedLibraryOption(optKey) {
					masked[optKey] = optValue
				} else {
					hasDefaults = true
				}
			}
			if hasDefaults {
				masked["<library-option-defaults>"] = "<masked>"
			}
			folder[key] = masked
		}
	}
	return value
}

// isPersistedLibraryOption reports whether a LibraryOptions field name matches
// one of the explicitly persisted option fields (case-insensitive). These are
// the fields the corpus persists via POST /Library/VirtualFolders/LibraryOptions
// and that must remain visible in the normalized comparison; all other
// LibraryOptions fields are host-local compatibility defaults and are masked.
func isPersistedLibraryOption(name string) bool {
	switch strings.ToLower(name) {
	case "enableinternetproviders", "savelocalmetadata":
		return true
	}
	return false
}

// normalizeQuickConnectVolatiles replaces only correctly typed root
// QuickConnect-result Secret, Code, and DateAdded fields. The pending
// QuickConnectResult DTO (upstream/jellyfin/MediaBrowser.Model/QuickConnect/
// QuickConnectResult.cs) carries the host-local secret and code plus a
// DateAdded timestamp set at initiation and again at authorization. Each host
// generates these independently, so they are host-local opaque identities and
// timing, not behavioral contract. The rule applies only to the root
// object. It preserves authentication, metadata, field presence, wrong
// types, nested fields, error bodies, and authentication results. Thus,
// mismatches remain comparable. The negotiated DTO casing is tolerated,
// but exact JSON keys win over recased DTO properties (matching the
// capture traversal).
func normalizeQuickConnectVolatiles(value any) any {
	result, ok := value.(map[string]any)
	if !ok || !isQuickConnectResult(result) {
		return value
	}
	for _, field := range []string{"Secret", "Code", "DateAdded"} {
		key, found := quickConnectField(result, field)
		if !found {
			continue
		}
		if _, isString := result[key].(string); isString {
			result[key] = quickConnectVolatileToken(field)
		}
		// Non-string value: keep the wrong type so the mismatch stays comparable.
	}
	return result
}

// isQuickConnectResult recognizes a root QuickConnect-result DTO by the
// presence of the Secret and Code fields that identify a pending request,
// plus the Authenticated boolean that is a defining field of the
// QuickConnectResult DTO. An error object that carries Secret, Code,
// Authenticated, and an error field does not match, so its body remains
// exact and a substantive error-body mismatch stays comparable.
func isQuickConnectResult(value map[string]any) bool {
	if _, hasError := quickConnectField(value, "error"); hasError {
		return false
	}
	_, hasSecret := quickConnectField(value, "Secret")
	_, hasCode := quickConnectField(value, "Code")
	if !hasSecret || !hasCode {
		return false
	}
	authenticatedKey, found := quickConnectField(value, "Authenticated")
	if !found {
		return false
	}
	_, isBool := value[authenticatedKey].(bool)
	return isBool
}

// quickConnectField finds a root field by exact key or the negotiated camel
// casing, mirroring captureObjectValue so a CamelCase-profile DTO satisfies the
// same traversal. It returns the matched key and a found flag. The caller
// replaces the exact key that the host used.
func quickConnectField(value map[string]any, name string) (string, bool) {
	if _, ok := value[name]; ok {
		return name, true
	}
	camel := captureCamelCase(name)
	if _, ok := value[camel]; ok {
		return camel, true
	}
	return "", false
}

// quickConnectVolatileToken returns the readable redaction tag for a
// normalized QuickConnect volatile field.
func quickConnectVolatileToken(field string) string {
	return "<quickconnect-" + strings.ToLower(field) + ">"
}

// normalizeProblemDetailsTrace replaces only the root traceId field in an
// ASP.NET ProblemDetails body. The traceId is a W3C traceparent generated
// per request and differs on every call, so it is request-local. The rule
// preserves type, title, status, and the errors map. It applies only to the
// root object. A non-object root is returned unchanged.
func normalizeProblemDetailsTrace(value any) any {
	result, ok := value.(map[string]any)
	if !ok {
		return value
	}
	if _, found := result["traceId"]; found {
		result["traceId"] = "<trace-id>"
	}
	return result
}

// isPortedExternalIdKey reports whether the given Key is one of the
// ExternalIdInfo values that the Deadfin ported-provider descriptor set
// produces (deadfin internal/providers/externalids.go ExternalIDDescriptors).
// Stock returns every registered IExternalId (upstream
// ProviderManager.cs:1002-1016), so an entry whose Key is outside this set is
// a descriptor for an unported provider. The normalizer removes such entries
// before comparison (divergence D-28). The normalizer also removes a spurious
// Deadfin descriptor outside this set. The Deadfin handler test pins the
// exact set, so the gate still catches it.
func isPortedExternalIdKey(key string) bool {
	switch key {
	case "Imdb", "Tmdb", "TmdbCollection",
		"MusicBrainzArtist", "MusicBrainzReleaseGroup", "MusicBrainzAlbumArtist",
		"MusicBrainzAlbum", "MusicBrainzRecording", "MusicBrainzTrack",
		"AudioDbArtist", "AudioDbAlbum":
		return true
	}
	return false
}

// normalizeExternalIdInfos filters a top-level JSON array of ExternalIdInfo
// objects to the pinned ported-descriptor set. It removes entries whose Key
// is outside the set that isPortedExternalIdKey defines. Entries inside the
// set stay byte-strict in every field (Name, Key, Type). A non-array root is
// left intact so a shape mismatch remains comparable. Array order is
// preserved so response-order differences stay visible. This action is
// divergence D-28: Deadfin returns descriptors for ported providers only,
// stock returns every registered IExternalId.
func normalizeExternalIdInfos(value any) any {
	array, ok := value.([]any)
	if !ok {
		return value
	}
	filtered := make([]any, 0, len(array))
	for _, entry := range array {
		object, ok := entry.(map[string]any)
		if !ok {
			filtered = append(filtered, entry)
			continue
		}
		key, ok := object["Key"].(string)
		if !ok {
			filtered = append(filtered, entry)
			continue
		}
		if isPortedExternalIdKey(key) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// remoteSearchResultSortKey builds the canonical sort key for one
// RemoteSearchResult: the sorted, lowercased "provider=value" ProviderIds
// pairs joined by "|", with a "Name|ProductionYear" fallback when ProviderIds
// is absent or empty. The key mirrors stock's dedup rule (upstream
// ProviderManager.cs:971). Membership, count, and every field stay strict.
// The normalizer changes only the order inside the group.
func remoteSearchResultSortKey(entry any) string {
	object, ok := entry.(map[string]any)
	if !ok {
		return ""
	}
	if providerIDs, ok := object["ProviderIds"].(map[string]any); ok && len(providerIDs) > 0 {
		pairs := make([]string, 0, len(providerIDs))
		for provider, value := range providerIDs {
			pairs = append(pairs, strings.ToLower(provider)+"="+fmt.Sprintf("%v", value))
		}
		sort.Strings(pairs)
		return strings.Join(pairs, "|")
	}
	name, _ := object["Name"].(string)
	year := ""
	if y, ok := object["ProductionYear"]; ok {
		year = fmt.Sprintf("%v", y)
	}
	return name + "|" + year
}

// normalizeRemoteSearchResultOrder sorts a top-level JSON array of
// RemoteSearchResult objects by the canonical key from
// remoteSearchResultSortKey. It absorbs only tie-order drift between stock's
// and Deadfin's differently-escaped MusicBrainz queries (divergence D-18).
// A non-array root is left intact. The sort is stable, so results that share a
// key keep their input order. Membership and count stay strict.
func normalizeRemoteSearchResultOrder(value any) any {
	array, ok := value.([]any)
	if !ok {
		return value
	}
	sort.SliceStable(array, func(i, j int) bool {
		return remoteSearchResultSortKey(array[i]) < remoteSearchResultSortKey(array[j])
	})
	return array
}
