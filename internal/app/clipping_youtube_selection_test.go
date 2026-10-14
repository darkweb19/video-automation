package app

import (
	"encoding/json"
	"errors"
	"testing"
)

const selectionTestVideoID = "abc123abc12"

func TestSelectYouTubeFormatsPrefersProgressiveMP4ThenWebM(t *testing.T) {
	formats := []youtubeFormat{
		selectionFormat("mp4-low", "mp4", "avc1", "mp4a", 360, "30", "1", "700", "0", "0", "0", ""),
		selectionFormat("webm-high", "webm", "vp9", "opus", 1080, "60", "10", "5000", "0", "0", "0", ""),
		selectionFormat("split-video-higher", "mp4", "avc1", "none", 1440, "60", "10", "7000", "0", "0", "0", ""),
		selectionFormat("split-audio", "m4a", "none", "mp4a", 0, "0", "0", "0", "256", "48000", "2", "en"),
		selectionFormat("mp4-high", "mp4", "avc1", "mp4a", 720, "30", "5", "2500", "0", "0", "0", ""),
	}

	selected, err := selectYouTubeFormats(formats, selectionTestVideoID)
	if err != nil {
		t.Fatalf("selectYouTubeFormats() error = %v", err)
	}
	if selected.Combined == nil || selected.Combined.FormatID != "mp4-high" || selected.Separate || selected.Video != nil || selected.Audio != nil {
		t.Fatalf("selected = %#v, want highest MP4 progressive format", selected)
	}
}

func TestSelectYouTubeFormatsUsesWebMFallbackAndStableTies(t *testing.T) {
	first := selectionFormat("webm-first", "webm", "vp9", "opus", 720, "30", "4", "1800", "0", "0", "0", "")
	second := selectionFormat("webm-second", "webm", "vp9", "opus", 720, "30", "4", "1800", "0", "0", "0", "")
	selected, err := selectYouTubeFormats([]youtubeFormat{first, second}, selectionTestVideoID)
	if err != nil {
		t.Fatalf("selectYouTubeFormats() error = %v", err)
	}
	if selected.Combined == nil || selected.Combined.FormatID != "webm-first" {
		t.Fatalf("selected = %#v, want first equal-ranked WebM candidate", selected)
	}
}

func TestSelectYouTubeFormatsChoosesHighestSplitVideoAndOriginalAudio(t *testing.T) {
	formats := []youtubeFormat{
		selectionFormat("video-low", "mp4", "avc1", "none", 720, "60", "10", "3000", "0", "0", "0", ""),
		selectionFormat("audio-dub", "mp4", "none", "mp4a", 0, "0", "0", "0", "256", "48000", "2", "de").withLanguagePreference("-1"),
		selectionFormat("video-high", "mp4", "avc1", "none", 1080, "30", "8", "4500", "0", "0", "0", ""),
		selectionFormat("audio-original-low", "mp4", "none", "mp4a", 0, "0", "0", "0", "128", "44100", "2", "en").withLanguagePreference("10"),
		selectionFormat("audio-original-high", "mp4", "none", "mp4a", 0, "0", "0", "0", "192", "48000", "2", "en").withLanguagePreference("10"),
	}

	selected, err := selectYouTubeFormats(formats, selectionTestVideoID)
	if err != nil {
		t.Fatalf("selectYouTubeFormats() error = %v", err)
	}
	if !selected.Separate || selected.Combined != nil || selected.Video == nil || selected.Audio == nil {
		t.Fatalf("selected = %#v, want separate video and audio", selected)
	}
	if selected.Video.FormatID != "video-high" {
		t.Errorf("selected video = %q, want highest-quality video", selected.Video.FormatID)
	}
	if selected.Audio.FormatID != "audio-original-high" {
		t.Errorf("selected audio = %q, want highest-ranked original audio", selected.Audio.FormatID)
	}
}

func TestSelectYouTubeFormatsRanksVideoDimensionsBeforeFrameRate(t *testing.T) {
	narrow := selectionFormat("narrow-fast", "mp4", "avc1", "none", 1080, "60", "8", "4000", "0", "0", "0", "")
	narrow.Width = 1440
	wide := selectionFormat("wide-slower", "mp4", "avc1", "none", 1080, "30", "8", "4000", "0", "0", "0", "")
	wide.Width = 1920
	formats := []youtubeFormat{
		narrow,
		wide,
		selectionFormat("audio", "m4a", "none", "mp4a", 0, "0", "0", "0", "128", "48000", "2", "en"),
	}

	selected, err := selectYouTubeFormats(formats, selectionTestVideoID)
	if err != nil {
		t.Fatalf("selectYouTubeFormats() error = %v", err)
	}
	if selected.Video == nil || selected.Video.FormatID != "wide-slower" {
		t.Fatalf("selected video = %#v, want greater pixel area before frame-rate ranking", selected.Video)
	}
}

func TestSelectYouTubeFormatsRanksNegativeQualityBelowKnownQuality(t *testing.T) {
	unknownDRC := selectionFormat("unknown-drc", "mp4", "avc1", "mp4a", 1080, "30", "-1.5", "4000", "0", "0", "0", "")
	unknown := selectionFormat("unknown", "mp4", "avc1", "mp4a", 1080, "30", "-1", "4000", "0", "0", "0", "")
	known := selectionFormat("known", "mp4", "avc1", "mp4a", 1080, "30", "0", "4000", "0", "0", "0", "")
	selected, err := selectYouTubeFormats([]youtubeFormat{unknownDRC, unknown, known}, selectionTestVideoID)
	if err != nil {
		t.Fatalf("selectYouTubeFormats() error = %v", err)
	}
	if selected.Combined == nil || selected.Combined.FormatID != "known" {
		t.Fatalf("selected = %#v, want the candidate with known quality", selected)
	}
	selected, err = selectYouTubeFormats([]youtubeFormat{unknownDRC}, selectionTestVideoID)
	if err != nil || selected.Combined == nil || selected.Combined.FormatID != "unknown-drc" {
		t.Fatalf("sole negative-quality candidate was not kept eligible: selected=%#v error=%v", selected, err)
	}
}

func TestSelectYouTubeFormatsRejectsDRMFormats(t *testing.T) {
	formats := []youtubeFormat{
		selectionFormat("drm-combined", "mp4", "avc1", "mp4a", 2160, "60", "10", "9000", "0", "0", "0", ""),
		selectionFormat("drm-video", "mp4", "avc1", "none", 1440, "60", "10", "7000", "0", "0", "0", ""),
		selectionFormat("video-safe", "mp4", "avc1", "none", 1080, "30", "8", "4500", "0", "0", "0", ""),
		selectionFormat("audio-drm-original", "m4a", "none", "mp4a", 0, "0", "0", "0", "256", "48000", "2", "en").withLanguagePreference("10"),
		selectionFormat("audio-safe-original", "m4a", "none", "mp4a", 0, "0", "0", "0", "128", "48000", "2", "en").withLanguagePreference("10"),
	}
	formats[0].HasDRM = true
	formats[1].HasDRM = true
	formats[3].HasDRM = true

	selected, err := selectYouTubeFormats(formats, selectionTestVideoID)
	if err != nil {
		t.Fatalf("selectYouTubeFormats() error = %v", err)
	}
	if selected.Combined != nil || selected.Video == nil || selected.Video.FormatID != "video-safe" || selected.Audio == nil || selected.Audio.FormatID != "audio-safe-original" {
		t.Fatalf("selected = %#v, want only the non-DRM split tracks including the unambiguous original", selected)
	}
}

func TestSelectYouTubeFormatsRejectsMalformedRankingMetadata(t *testing.T) {
	badNaN := selectionFormat("video-nan", "webm", "vp9", "none", 1440, "NaN", "8", "4000", "0", "0", "0", "")
	badInfinity := selectionFormat("video-infinity", "webm", "vp9", "none", 1440, "30", "8", "+Inf", "0", "0", "0", "")
	badNonFiniteQuality := selectionFormat("video-nan-quality", "webm", "vp9", "none", 2160, "30", "NaN", "4000", "0", "0", "0", "")
	badNegativeMetric := selectionFormat("video-negative-tbr", "webm", "vp9", "none", 1440, "30", "8", "-1", "0", "0", "0", "")
	badNegativeFPS := selectionFormat("video-negative-fps", "webm", "vp9", "none", 1440, "-1", "8", "4000", "0", "0", "0", "")
	badNegativeDimension := selectionFormat("video-negative-width", "webm", "vp9", "none", 1440, "30", "8", "4000", "0", "0", "0", "")
	badNegativeDimension.Width = -10
	badZeroDimension := selectionFormat("video-zero-width", "webm", "vp9", "none", 1440, "30", "8", "4000", "0", "0", "0", "")
	badZeroDimension.Width = 0
	badAudio := selectionFormat("audio-negative", "m4a", "none", "mp4a", 0, "0", "0", "0", "-1", "48000", "2", "en").withLanguagePreference("10")
	badNegativeSampleRate := selectionFormat("audio-negative-sample-rate", "m4a", "none", "mp4a", 0, "0", "0", "0", "128", "-1", "2", "en").withLanguagePreference("10")
	badNegativeChannels := selectionFormat("audio-negative-channels", "m4a", "none", "mp4a", 0, "0", "0", "0", "128", "48000", "-1", "en").withLanguagePreference("10")
	formats := []youtubeFormat{
		badNaN,
		badInfinity,
		badNonFiniteQuality,
		badNegativeMetric,
		badNegativeFPS,
		badNegativeDimension,
		badZeroDimension,
		selectionFormat("video-valid", "webm", "vp9", "none", 720, "30", "4", "2000", "0", "0", "0", ""),
		badAudio,
		badNegativeSampleRate,
		badNegativeChannels,
		selectionFormat("audio-valid-original", "m4a", "none", "mp4a", 0, "0", "0", "0", "128", "48000", "2", "en").withLanguagePreference("10"),
	}

	selected, err := selectYouTubeFormats(formats, selectionTestVideoID)
	if err != nil {
		t.Fatalf("selectYouTubeFormats() error = %v", err)
	}
	if selected.Video == nil || selected.Video.FormatID != "video-valid" || selected.Audio == nil || selected.Audio.FormatID != "audio-valid-original" {
		t.Fatalf("selected = %#v, want only candidates with valid ranking metadata", selected)
	}
}

func TestSelectYouTubeFormatsRejectsAmbiguousAudioWithoutOriginalMarker(t *testing.T) {
	formats := []youtubeFormat{
		selectionFormat("video", "webm", "vp9", "none", 1080, "30", "8", "3000", "0", "0", "0", ""),
		selectionFormat("audio-en", "webm", "none", "opus", 0, "0", "0", "0", "128", "48000", "2", "en"),
		selectionFormat("audio-fr", "webm", "none", "opus", 0, "0", "0", "0", "256", "48000", "2", "fr"),
	}

	_, err := selectYouTubeFormats(formats, selectionTestVideoID)
	if !errors.Is(err, errYouTubeOriginalAudioUnavailable) {
		t.Fatalf("selectYouTubeFormats() error = %v, want generic original-audio-unavailable error", err)
	}
}

func TestSelectYouTubeFormatsDoesNotDowngradeToDirectAudioWhenOriginalIsManifestOnly(t *testing.T) {
	video := selectionFormat("video", "mp4", "avc1", "none", 1080, "30", "8", "3000", "0", "0", "0", "")
	directDub := selectionFormat("direct-hindi", "m4a", "none", "mp4a", 0, "0", "0", "0", "160", "48000", "2", "hi").withLanguagePreference("-1")
	manifestOriginal := selectionFormat("manifest-original", "m4a", "none", "mp4a", 0, "0", "0", "0", "192", "48000", "2", "en").withLanguagePreference("10")
	manifestOriginal.Protocol = "http_dash_segments"

	_, err := selectYouTubeFormats([]youtubeFormat{video, directDub, manifestOriginal}, selectionTestVideoID)
	if !errors.Is(err, errYouTubeOriginalAudioUnavailable) {
		t.Fatalf("selectYouTubeFormats() error = %v, want unavailable-original error", err)
	}
}

func TestSelectYouTubeFormatsRejectsCombinedManifestOriginalWhenDirectAudioIsAlternate(t *testing.T) {
	video := selectionFormat("video", "mp4", "avc1", "none", 1080, "30", "8", "3000", "0", "0", "0", "")
	directAlternate := selectionFormat("direct-hindi", "m4a", "none", "mp4a", 0, "0", "0", "0", "160", "48000", "2", "hi").withLanguagePreference("-1")
	manifestOriginal := selectionFormat("combined-manifest-original", "mp4", "avc1", "mp4a", 1080, "30", "8", "3000", "192", "48000", "2", "en").withLanguagePreference("10")
	manifestOriginal.Protocol = "http_dash_segments"

	_, err := selectYouTubeFormats([]youtubeFormat{video, directAlternate, manifestOriginal}, selectionTestVideoID)
	if !errors.Is(err, errYouTubeOriginalAudioUnavailable) {
		t.Fatalf("selectYouTubeFormats() error = %v, want unavailable-original error", err)
	}
}

func TestSelectYouTubeFormatsAllowsOnlyAvailableAudioLanguage(t *testing.T) {
	formats := []youtubeFormat{
		selectionFormat("video", "mp4", "avc1", "none", 1080, "30", "8", "3000", "0", "0", "0", ""),
		selectionFormat("audio-only", "mp4", "none", "mp4a", 0, "0", "0", "0", "128", "48000", "2", "en"),
	}

	selected, err := selectYouTubeFormats(formats, selectionTestVideoID)
	if err != nil {
		t.Fatalf("selectYouTubeFormats() error = %v", err)
	}
	if selected.Audio == nil || selected.Audio.FormatID != "audio-only" {
		t.Fatalf("selected audio = %#v, want sole available track", selected.Audio)
	}
}

func TestSelectYouTubeFormatsAllowsM4AAudioAndCrossContainerTracks(t *testing.T) {
	formats := []youtubeFormat{
		selectionFormat("video-webm", "webm", "vp9", "none", 1080, "30", "8", "3000", "0", "0", "0", ""),
		selectionFormat("audio-m4a", "m4a", "none", "mp4a", 0, "0", "0", "0", "128", "48000", "2", "en"),
	}

	selected, err := selectYouTubeFormats(formats, selectionTestVideoID)
	if err != nil {
		t.Fatalf("selectYouTubeFormats() error = %v", err)
	}
	if selected.Audio == nil || selected.Audio.FormatID != "audio-m4a" || selected.Video.FormatID != "video-webm" {
		t.Fatalf("selected pair = %#v, want WebM video and compatible M4A audio", selected)
	}
}

func TestSelectYouTubeFormatsRejectsUnmarkedExplicitDub(t *testing.T) {
	formats := []youtubeFormat{
		selectionFormat("video", "mp4", "avc1", "none", 1080, "30", "8", "3000", "0", "0", "0", ""),
		selectionFormat("audio-dub", "m4a", "none", "mp4a", 0, "0", "0", "0", "128", "48000", "2", "en"),
	}
	formats[1].FormatNote = "English (auto-dubbed)"

	_, err := selectYouTubeFormats(formats, selectionTestVideoID)
	if !errors.Is(err, errYouTubeOriginalAudioUnavailable) {
		t.Fatalf("selectYouTubeFormats() error = %v, want generic original-audio-unavailable error", err)
	}
}

func TestSelectYouTubeFormatsFailsWithoutUsableFormats(t *testing.T) {
	formats := []youtubeFormat{
		selectionFormat("insecure", "mp4", "avc1", "mp4a", 1080, "30", "8", "3000", "0", "0", "0", ""),
		selectionFormat("unsupported-container", "m4a", "none", "mp4a", 0, "0", "0", "0", "128", "48000", "2", "en"),
	}
	formats[0].URL = "http://r1.googlevideo.com/videoplayback"

	_, err := selectYouTubeFormats(formats, selectionTestVideoID)
	if !errors.Is(err, errYouTubeFormatsUnavailable) {
		t.Fatalf("selectYouTubeFormats() error = %v, want generic unsupported-format error", err)
	}
}

func TestSelectYouTubeFormatsKeepsSplitTiesStable(t *testing.T) {
	formats := []youtubeFormat{
		selectionFormat("video-first", "webm", "vp9", "none", 1080, "30", "8", "3000", "0", "0", "0", ""),
		selectionFormat("video-second", "webm", "vp9", "none", 1080, "30", "8", "3000", "0", "0", "0", ""),
		selectionFormat("audio-first", "webm", "none", "opus", 0, "0", "0", "0", "128", "48000", "2", "en"),
		selectionFormat("audio-second", "webm", "none", "opus", 0, "0", "0", "0", "128", "48000", "2", "en"),
	}

	selected, err := selectYouTubeFormats(formats, selectionTestVideoID)
	if err != nil {
		t.Fatalf("selectYouTubeFormats() error = %v", err)
	}
	if selected.Video.FormatID != "video-first" || selected.Audio.FormatID != "audio-first" {
		t.Fatalf("selected pair = (%q, %q), want first equal-ranked candidates", selected.Video.FormatID, selected.Audio.FormatID)
	}
}

func selectionFormat(id, extension, videoCodec, audioCodec string, height int, fps, quality, tbr, abr, asr, channels, language string) youtubeFormat {
	return youtubeFormat{
		FormatID:           id,
		AudioCodec:         audioCodec,
		VideoCodec:         videoCodec,
		Protocol:           "https",
		Extension:          extension,
		URL:                "https://r1.googlevideo.com/videoplayback?id=synthetic",
		Width:              height * 16 / 9,
		Height:             height,
		FPS:                json.Number(fps),
		Quality:            json.Number(quality),
		TBR:                json.Number(tbr),
		ABR:                json.Number(abr),
		ASR:                json.Number(asr),
		AudioChannels:      json.Number(channels),
		Language:           language,
		LanguagePreference: json.Number("-1"),
	}
}

func (format youtubeFormat) withLanguagePreference(preference string) youtubeFormat {
	format.LanguagePreference = json.Number(preference)
	return format
}
