// SPDX-License-Identifier: GPL-2.0-only

package playback

// Faithful port of MediaBrowser.Model/Extensions/ContainerHelper.cs @ b04614e.
//
// Semantics: profile lists are comma-delimited; a leading '-' inverts the
// list; an empty/nil profile accepts everything; comparisons are
// case-insensitive; empty segments on either side are skipped.
//
// Evaluation-order quirks preserved from upstream (they differ by overload):
//   - ContainsContainer("" or nil profile, input): the '-' strip happens
//     first, so a profile of just "-" becomes "" and accepts everything.
//   - Empty INPUT returns isNegativeList (checked before the profile-empty
//     check in the string overload): profile "" + input "" => false,
//     profile "-x" + input "" => true, profile "x" + input "" => false.

import "strings"

// ContainsContainer reports whether any container in inputContainer appears in
// the comma-delimited profileContainers list ('-' prefix inverts).
// Ports ContainerHelper.ContainsContainer(string?, string?).
func ContainsContainer(profileContainers, inputContainer string) bool {
	isNegative := false
	if strings.HasPrefix(profileContainers, "-") {
		isNegative = true
		profileContainers = profileContainers[1:]
	}
	return containsContainerWithNegation(profileContainers, isNegative, inputContainer)
}

// containsContainerWithNegation ports
// ContainerHelper.ContainsContainer(string?, bool, string?).
func containsContainerWithNegation(profileContainers string, isNegativeList bool, inputContainer string) bool {
	if inputContainer == "" {
		return isNegativeList
	}
	if profileContainers == "" {
		// Empty profiles always support all containers/codecs.
		return true
	}
	for input := range strings.SplitSeq(inputContainer, ",") {
		if input == "" {
			continue
		}
		for profile := range strings.SplitSeq(profileContainers, ",") {
			if profile != "" && strings.EqualFold(input, profile) {
				return !isNegativeList
			}
		}
	}
	return isNegativeList
}

// ContainsContainerList ports
// ContainerHelper.ContainsContainer(IReadOnlyList<string>?, bool, string).
// A nil list accepts everything; empty input segments are discarded (so empty
// input returns isNegativeList). Note: unlike the string overload, empty
// entries in profileContainers are NOT skipped upstream — but empty input
// segments are removed, so they can never match.
func ContainsContainerList(profileContainers []string, isNegativeList bool, inputContainer string) bool {
	if profileContainers == nil {
		// Empty profiles always support all containers/codecs.
		return true
	}
	for input := range strings.SplitSeq(inputContainer, ",") {
		if input == "" { // StringSplitOptions.RemoveEmptyEntries
			continue
		}
		for _, profile := range profileContainers {
			if strings.EqualFold(profile, input) {
				return !isNegativeList
			}
		}
	}
	return isNegativeList
}

// SplitContainers ports ContainerHelper.Split: comma split with empty entries
// removed; nil-safe.
func SplitContainers(input string) []string {
	if input == "" {
		return []string{}
	}
	var out []string
	for part := range strings.SplitSeq(input, ",") {
		if part != "" {
			out = append(out, part)
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}
