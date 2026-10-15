// SPDX-License-Identifier: GPL-2.0-only

package playback

// Tests port the semantics of ConditionProcessor.cs @ b04614e. Line references
// point into that file.

import (
	"encoding/json"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func cond(ct ConditionType, prop ConditionProperty, value string, required bool) ProfileCondition {
	return ProfileCondition{Condition: ct, Property: prop, Value: value, IsRequired: required}
}

// video evaluates a video condition and fails the test on unexpected error.
func video(t *testing.T, c ProfileCondition, in VideoConditionInput) bool {
	t.Helper()
	ok, err := IsVideoConditionSatisfied(c, in)
	if err != nil {
		t.Fatalf("unexpected error for %s %s %q: %v", c.Condition, c.Property, c.Value, err)
	}
	return ok
}

func TestNilValueSatisfiesUnlessRequired(t *testing.T) {
	// ConditionProcessor.cs:192-196 and the equivalent guard in every overload.
	props := []ConditionProperty{
		PropWidth, PropVideoLevel, PropVideoProfile, PropIsInterlaced,
		PropVideoTimestamp, PropVideoRangeType, PropVideoFramerate, PropVideoCodecTag,
	}
	for _, p := range props {
		if !video(t, cond(ConditionEquals, p, "42", false), VideoConditionInput{}) {
			t.Errorf("%s: nil value with IsRequired=false must satisfy", p)
		}
		if video(t, cond(ConditionEquals, p, "42", true), VideoConditionInput{}) {
			t.Errorf("%s: nil value with IsRequired=true must not satisfy", p)
		}
	}
}

func TestIntConditions(t *testing.T) {
	in := VideoConditionInput{Width: ptr(1920)}
	cases := []struct {
		c    ProfileCondition
		want bool
	}{
		{cond(ConditionEquals, PropWidth, "1920", true), true},
		{cond(ConditionEquals, PropWidth, "1280", true), false},
		{cond(ConditionNotEquals, PropWidth, "1280", true), true},
		{cond(ConditionNotEquals, PropWidth, "1920", true), false},
		{cond(ConditionLessThanEqual, PropWidth, "1920", true), true},
		{cond(ConditionLessThanEqual, PropWidth, "1919", true), false},
		{cond(ConditionGreaterThanEqual, PropWidth, "3840", true), false},
		{cond(ConditionGreaterThanEqual, PropWidth, "1920", true), true},
		// EqualsAny splits on '|' (:199-211)
		{cond(ConditionEqualsAny, PropWidth, "1280|1920|3840", true), true},
		{cond(ConditionEqualsAny, PropWidth, "1280|720", true), false},
		// Unparseable EqualsAny element is skipped, not fatal
		{cond(ConditionEqualsAny, PropWidth, "abc|1920", true), true},
		// Whole-value parse failure => false, no error (:213-230)
		{cond(ConditionEquals, PropWidth, "not-a-number", true), false},
		// NumberStyles.Integer: surrounding whitespace + leading sign
		{cond(ConditionEquals, PropWidth, " 1920 ", true), true},
		{cond(ConditionEquals, PropWidth, "+1920", true), true},
		// Hex/decimal-point are NOT Integer style
		{cond(ConditionEquals, PropWidth, "1920.0", true), false},
	}
	for _, tc := range cases {
		if got := video(t, tc.c, in); got != tc.want {
			t.Errorf("%s %s %q: got %v want %v", tc.c.Condition, tc.c.Property, tc.c.Value, got, tc.want)
		}
	}

	// NumStreams is non-nullable upstream (plain int parameter).
	if video(t, cond(ConditionLessThanEqual, PropNumStreams, "32", true), VideoConditionInput{NumStreams: 33}) {
		t.Error("NumStreams 33 must fail <=32")
	}
}

func TestStringConditions(t *testing.T) {
	in := VideoConditionInput{VideoProfile: ptr("High")}
	cases := []struct {
		c    ProfileCondition
		want bool
	}{
		// OrdinalIgnoreCase (:246-250)
		{cond(ConditionEquals, PropVideoProfile, "high", true), true},
		{cond(ConditionEquals, PropVideoProfile, "HIGH", true), true},
		{cond(ConditionNotEquals, PropVideoProfile, "main", true), true},
		{cond(ConditionNotEquals, PropVideoProfile, "High", true), false},
		{cond(ConditionEqualsAny, PropVideoProfile, "baseline|main|high", true), true},
		{cond(ConditionEqualsAny, PropVideoProfile, "baseline|main", true), false},
	}
	for _, tc := range cases {
		if got := video(t, tc.c, in); got != tc.want {
			t.Errorf("%s %q vs High: got %v want %v", tc.c.Condition, tc.c.Value, got, tc.want)
		}
	}

	// Empty string counts as null (string.IsNullOrEmpty, :235)
	empty := VideoConditionInput{VideoProfile: ptr("")}
	if !video(t, cond(ConditionEquals, PropVideoProfile, "high", false), empty) {
		t.Error("empty string with IsRequired=false must satisfy")
	}
	if video(t, cond(ConditionEquals, PropVideoProfile, "high", true), empty) {
		t.Error("empty string with IsRequired=true must not satisfy")
	}

	// Range operators on strings throw upstream (:251-252) => error here.
	if _, err := IsVideoConditionSatisfied(cond(ConditionLessThanEqual, PropVideoProfile, "high", true), in); err == nil {
		t.Error("LessThanEqual on a string property must error")
	}
}

func TestBoolConditions(t *testing.T) {
	in := VideoConditionInput{IsInterlaced: ptr(true)}
	cases := []struct {
		c    ProfileCondition
		want bool
	}{
		{cond(ConditionEquals, PropIsInterlaced, "true", true), true},
		{cond(ConditionEquals, PropIsInterlaced, "True", true), true}, // bool.TryParse is case-insensitive
		{cond(ConditionEquals, PropIsInterlaced, " TRUE ", true), true},
		{cond(ConditionNotEquals, PropIsInterlaced, "false", true), true},
		{cond(ConditionEquals, PropIsInterlaced, "false", true), false},
		// bool.TryParse does NOT accept 1/0/t/f (unlike Go's ParseBool) => parse failure => false (:264,277)
		{cond(ConditionEquals, PropIsInterlaced, "1", true), false},
		{cond(ConditionEquals, PropIsInterlaced, "t", true), false},
	}
	for _, tc := range cases {
		if got := video(t, tc.c, in); got != tc.want {
			t.Errorf("bool %s %q: got %v want %v", tc.c.Condition, tc.c.Value, got, tc.want)
		}
	}
	// EqualsAny on bool: upstream parses the value BEFORE the switch, so
	// "true|false" fails bool.TryParse and returns false without throwing
	// (:264 guard). Only a parseable value reaches the throwing default arm.
	if got := video(t, cond(ConditionEqualsAny, PropIsInterlaced, "true|false", true), in); got {
		t.Error(`EqualsAny "true|false" must be false (parse failure), not an error`)
	}
	if _, err := IsVideoConditionSatisfied(cond(ConditionEqualsAny, PropIsInterlaced, "true", true), in); err == nil {
		t.Error(`EqualsAny with parseable bool value "true" must error (default: arm, :272-273)`)
	}
}

func TestDoubleConditions(t *testing.T) {
	in := VideoConditionInput{VideoLevel: ptr(41.0)}
	cases := []struct {
		c    ProfileCondition
		want bool
	}{
		{cond(ConditionEquals, PropVideoLevel, "41", true), true},
		{cond(ConditionLessThanEqual, PropVideoLevel, "41", true), true},
		{cond(ConditionGreaterThanEqual, PropVideoLevel, "42", true), false},
		{cond(ConditionEqualsAny, PropVideoLevel, "30|41|51", true), true},
		{cond(ConditionEqualsAny, PropVideoLevel, "30|51", true), false},
		// NumberStyles.AllowThousands: ',' group separators accepted
		{cond(ConditionEquals, PropVideoLevel, "4,1", true), true}, // "4,1" -> 41 (groups are not validated)
		// exponent accepted (NumberStyles.Float)
		{cond(ConditionEquals, PropVideoLevel, "4.1e1", true), true},
		{cond(ConditionEquals, PropVideoLevel, "junk", true), false},
	}
	for _, tc := range cases {
		if got := video(t, tc.c, in); got != tc.want {
			t.Errorf("double %s %q: got %v want %v", tc.c.Condition, tc.c.Value, got, tc.want)
		}
	}
}

// TestFramerateFloatWidening pins the C# float->double widening semantics:
// IsVideoConditionSatisfied receives float? videoFramerate and passes it to
// the double? comparator. float32(23.976) widens to 23.975999832153320, which
// does NOT equal the parsed double 23.976. GreaterThanEqual/LessThanEqual
// behave "normally". A port that stores framerate as float64 would silently
// diverge here.
func TestFramerateFloatWidening(t *testing.T) {
	in := VideoConditionInput{VideoFramerate: ptr(float32(23.976))}
	if video(t, cond(ConditionEquals, PropVideoFramerate, "23.976", true), in) {
		t.Error("float-widened 23.976 must NOT Equal double 23.976 (C# widening semantics)")
	}
	if !video(t, cond(ConditionLessThanEqual, PropVideoFramerate, "23.976", true), in) {
		t.Error("float-widened 23.976 must satisfy <= 23.976")
	}
	if !video(t, cond(ConditionGreaterThanEqual, PropVideoFramerate, "23.9", true), in) {
		t.Error("float-widened 23.976 must satisfy >= 23.9")
	}
	// Round float values compare cleanly.
	exact := VideoConditionInput{VideoFramerate: ptr(float32(25))}
	if !video(t, cond(ConditionEquals, PropVideoFramerate, "25", true), exact) {
		t.Error("float 25 must Equal 25")
	}
}

func TestTimestampConditions(t *testing.T) {
	in := VideoConditionInput{Timestamp: ptr(TimestampValid)}
	if !video(t, cond(ConditionEquals, PropVideoTimestamp, "Valid", true), in) {
		t.Error("Valid == Valid")
	}
	if !video(t, cond(ConditionEquals, PropVideoTimestamp, "valid", true), in) {
		t.Error("Enum.Parse ignoreCase")
	}
	if video(t, cond(ConditionNotEquals, PropVideoTimestamp, "Valid", true), in) {
		t.Error("NotEquals Valid must fail")
	}
	// Upstream uses throwing Enum.Parse (:331): invalid value => error.
	if _, err := IsVideoConditionSatisfied(cond(ConditionEquals, PropVideoTimestamp, "Bogus", true), in); err == nil {
		t.Error("invalid timestamp value must error (upstream throws)")
	}
	// EqualsAny on timestamp throws upstream (default: arm, :339-340).
	if _, err := IsVideoConditionSatisfied(cond(ConditionEqualsAny, PropVideoTimestamp, "None|Valid", true), in); err == nil {
		t.Error("EqualsAny on timestamp must error")
	}
}

func TestVideoRangeTypeConditions(t *testing.T) {
	hdr10 := VideoConditionInput{VideoRangeType: ptr(VideoRangeHDR10)}
	plus := VideoConditionInput{VideoRangeType: ptr(VideoRangeHDR10Plus)}
	unknown := VideoConditionInput{VideoRangeType: ptr(VideoRangeUnknown)}

	cases := []struct {
		name string
		c    ProfileCondition
		in   VideoConditionInput
		want bool
	}{
		{"equals self", cond(ConditionEquals, PropVideoRangeType, "HDR10", true), hdr10, true},
		{"case-insensitive", cond(ConditionEquals, PropVideoRangeType, "hdr10", true), hdr10, true},
		{"equalsany", cond(ConditionEqualsAny, PropVideoRangeType, "SDR|HDR10|HLG", true), hdr10, true},
		{"equalsany miss", cond(ConditionEqualsAny, PropVideoRangeType, "SDR|HLG", true), hdr10, false},
		// HDR10Plus satisfies HDR10 conditions (:352-359)
		{"plus satisfies hdr10", cond(ConditionEquals, PropVideoRangeType, "HDR10", true), plus, true},
		{"plus satisfies hdr10 in any", cond(ConditionEqualsAny, PropVideoRangeType, "SDR|HDR10", true), plus, true},
		{"plus satisfies plus", cond(ConditionEquals, PropVideoRangeType, "HDR10Plus", true), plus, true},
		// Upstream quirk preserved: NotEquals HDR10Plus passes for HDR10Plus,
		// because the HDR10 pre-check ("HDR10 != HDR10Plus" == true) wins.
		{"quirk: notequals plus passes for plus", cond(ConditionNotEquals, PropVideoRangeType, "HDR10Plus", true), plus, true},
		// ...but NotEquals HDR10 correctly fails for HDR10Plus (pre-check
		// evaluates "HDR10 != HDR10" false, then main check "HDR10Plus !=
		// HDR10" true) — wait, that's TRUE upstream. Verify the chain:
		// pre-check false, main: HDR10Plus != HDR10 => true.
		{"notequals hdr10 for plus", cond(ConditionNotEquals, PropVideoRangeType, "HDR10", true), plus, true},
		{"notequals hdr10 for hdr10", cond(ConditionNotEquals, PropVideoRangeType, "HDR10", true), hdr10, false},
		// Unknown is treated as nil (:346)
		{"unknown !required", cond(ConditionEquals, PropVideoRangeType, "SDR", false), unknown, true},
		{"unknown required", cond(ConditionEquals, PropVideoRangeType, "SDR", true), unknown, false},
		// Unparseable value => false (TryParse path, :376-386)
		{"bogus value", cond(ConditionEquals, PropVideoRangeType, "Bogus", true), hdr10, false},
	}
	for _, tc := range cases {
		if got := video(t, tc.c, tc.in); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestUnhandledVideoPropertyReturnsTrue(t *testing.T) {
	// ConditionProcessor.cs:100-101 — e.g. Has64BitOffsets is not handled by
	// the video switch and silently passes.
	in := VideoConditionInput{Width: ptr(100)}
	if !video(t, cond(ConditionEquals, PropHas64BitOffsets, "true", true), in) {
		t.Error("unhandled property must return true")
	}
}

func TestImageAndAudioUnexpectedPropertiesError(t *testing.T) {
	// Image: only Width/Height are legal (:121).
	if _, err := IsImageConditionSatisfied(cond(ConditionEquals, PropVideoLevel, "41", true), ptr(1), ptr(1)); err == nil {
		t.Error("image condition on VideoLevel must error")
	}
	ok, err := IsImageConditionSatisfied(cond(ConditionLessThanEqual, PropWidth, "4096", true), ptr(1920), ptr(1080))
	if err != nil || !ok {
		t.Errorf("image width condition: got %v, %v", ok, err)
	}

	// Audio: only the four audio props (:147).
	if _, err := IsAudioConditionSatisfied(cond(ConditionEquals, PropWidth, "1", true), ptr(2), nil, nil, nil); err == nil {
		t.Error("audio condition on Width must error")
	}
	ok, err = IsAudioConditionSatisfied(cond(ConditionLessThanEqual, PropAudioChannels, "2", true), ptr(2), nil, nil, nil)
	if err != nil || !ok {
		t.Errorf("audio channels condition: got %v, %v", ok, err)
	}

	// VideoAudio: AudioProfile + IsSecondaryAudio are legal here.
	ok, err = IsVideoAudioConditionSatisfied(
		cond(ConditionEqualsAny, PropAudioProfile, "lc|he-aac", true),
		VideoAudioConditionInput{AudioProfile: ptr("LC")})
	if err != nil || !ok {
		t.Errorf("videoaudio profile condition: got %v, %v", ok, err)
	}
	ok, err = IsVideoAudioConditionSatisfied(
		cond(ConditionEquals, PropIsSecondaryAudio, "false", true),
		VideoAudioConditionInput{IsSecondaryTrack: ptr(false)})
	if err != nil || !ok {
		t.Errorf("videoaudio secondary condition: got %v, %v", ok, err)
	}
}

func TestProfileConditionJSONDefaults(t *testing.T) {
	// C# parameterless ctor sets IsRequired = true, so JSON omitting the
	// field must decode to true (ProfileCondition.cs ctor).
	var c ProfileCondition
	if err := json.Unmarshal([]byte(`{"Condition":"EqualsAny","Property":"VideoLevel","Value":"41"}`), &c); err != nil {
		t.Fatal(err)
	}
	if !c.IsRequired {
		t.Error("IsRequired must default to true when absent from JSON")
	}
	if c.Condition != ConditionEqualsAny || c.Property != PropVideoLevel || c.Value != "41" {
		t.Errorf("decoded %+v", c)
	}

	// Explicit false is honored; enum casing is forgiven; numeric enums accepted.
	if err := json.Unmarshal([]byte(`{"Condition":"equalsANY","Property":24,"Value":"SDR","IsRequired":false}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.IsRequired {
		t.Error("explicit IsRequired=false must be honored")
	}
	if c.Property != PropVideoRangeType {
		t.Errorf("numeric enum Property: got %v", c.Property)
	}

	// Round-trip writes .NET names.
	out, err := json.Marshal(ProfileCondition{Condition: ConditionNotEquals, Property: PropVideoRangeType, Value: "DOVI", IsRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"Condition":"NotEquals","Property":"VideoRangeType","Value":"DOVI","IsRequired":true}`
	if string(out) != want {
		t.Errorf("marshal: got %s want %s", out, want)
	}
}

// int.TryParse fails outside the Int32 range; the condition then does not
// apply instead of comparing against a wrapped value.
func TestParseIntInvariantInt32Range(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
	}{
		{" 42 ", 42, true},
		{"-2147483648", -2147483648, true},
		{"2147483647", 2147483647, true},
		{"2147483648", 0, false},
		{"-2147483649", 0, false},
		{"9223372036854775807", 0, false},
	} {
		got, ok := parseIntInvariant(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseIntInvariant(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
