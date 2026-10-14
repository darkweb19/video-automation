package app

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"net/url"
	"strconv"
	"strings"
)

const youtubeOriginalLanguagePreference = 10

var (
	errYouTubeFormatsUnavailable       = errors.New("no supported YouTube formats are available")
	errYouTubeOriginalAudioUnavailable = errors.New("original YouTube audio is unavailable")
)

// youtubeFormatSelection contains only the formats needed for the selected
// path. Format URLs are retained in memory for the existing guarded downloader
// and are never included in errors or diagnostics.
type youtubeFormatSelection struct {
	Combined *youtubeFormat
	Video    *youtubeFormat
	Audio    *youtubeFormat
	Separate bool
}

// selectYouTubeFormats preserves the progressive MP4 then WebM preference. It
// considers separate tracks only when no eligible progressive format exists.
// expectedID is used to validate extractor-supplied Referer headers.
func selectYouTubeFormats(formats []youtubeFormat, expectedID string) (youtubeFormatSelection, error) {
	if !validYouTubeVideoID(expectedID) {
		return youtubeFormatSelection{}, errYouTubeFormatsUnavailable
	}

	for _, extension := range []string{"mp4", "webm"} {
		var best *youtubeFormat
		for i := range formats {
			format := &formats[i]
			if !eligibleYouTubeFormat(*format, expectedID, youtubeFormatCombined) || normalizedYouTubeExtension(format.Extension) != extension {
				continue
			}
			if best == nil || compareYouTubeVideoFormats(*format, *best) > 0 {
				best = format
			}
		}
		if best != nil {
			selected := *best
			return youtubeFormatSelection{Combined: &selected}, nil
		}
	}

	var bestVideo *youtubeFormat
	for i := range formats {
		format := &formats[i]
		if !eligibleYouTubeFormat(*format, expectedID, youtubeFormatVideo) {
			continue
		}
		if bestVideo == nil || compareYouTubeVideoFormats(*format, *bestVideo) > 0 {
			bestVideo = format
		}
	}
	if bestVideo == nil {
		return youtubeFormatSelection{}, errYouTubeFormatsUnavailable
	}

	// Matroska is the fixed merge container; FFmpeg stream-copy and the later
	// probe validate codec/container compatibility without a quality downgrade.
	var allAudioFormats []*youtubeFormat
	var audioFormats []*youtubeFormat
	for i := range formats {
		format := &formats[i]
		if youtubeCodecPresent(format.AudioCodec) {
			// Keep every entry carrying audio metadata in the ambiguity check,
			// including combined HLS/DASH formats. An Original track can be
			// advertised only as combined manifest media while a different
			// language remains directly downloadable as audio-only.
			allAudioFormats = append(allAudioFormats, format)
			if !youtubeCodecPresent(format.VideoCodec) && eligibleYouTubeFormat(*format, expectedID, youtubeFormatAudio) {
				audioFormats = append(audioFormats, format)
			}
		}
	}
	if len(audioFormats) == 0 {
		return youtubeFormatSelection{}, errYouTubeOriginalAudioUnavailable
	}

	originals := make([]*youtubeFormat, 0, len(audioFormats))
	allOriginals := false
	for _, format := range allAudioFormats {
		if youtubeFormatLanguagePreference(format.LanguagePreference) == youtubeOriginalLanguagePreference {
			allOriginals = true
			if eligibleYouTubeFormat(*format, expectedID, youtubeFormatAudio) {
				originals = append(originals, format)
			}
		}
	}

	// With no Original marker, one language group and no explicit dub note is
	// the only unambiguous fallback audio choice.
	selectedAudio := audioFormats
	if allOriginals && len(originals) == 0 {
		return youtubeFormatSelection{}, errYouTubeOriginalAudioUnavailable
	}
	if len(originals) > 0 {
		selectedAudio = originals
	} else if distinctYouTubeAudioLanguages(allAudioFormats) > 1 || hasYouTubeAudioDubNote(allAudioFormats) {
		return youtubeFormatSelection{}, errYouTubeOriginalAudioUnavailable
	}

	bestAudio := selectedAudio[0]
	for _, format := range selectedAudio[1:] {
		if compareYouTubeAudioFormats(*format, *bestAudio) > 0 {
			bestAudio = format
		}
	}
	videoCopy, audioCopy := *bestVideo, *bestAudio
	return youtubeFormatSelection{Video: &videoCopy, Audio: &audioCopy, Separate: true}, nil
}

type youtubeFormatRole uint8

const (
	youtubeFormatCombined youtubeFormatRole = iota
	youtubeFormatVideo
	youtubeFormatAudio
)

func eligibleYouTubeFormat(format youtubeFormat, expectedID string, role youtubeFormatRole) bool {
	if format.HasDRM || !strings.EqualFold(strings.TrimSpace(format.Protocol), "https") || !supportedYouTubeSelectionExtension(format.Extension, role) {
		return false
	}
	videoPresent := youtubeCodecPresent(format.VideoCodec)
	audioPresent := youtubeCodecPresent(format.AudioCodec)
	switch role {
	case youtubeFormatCombined:
		if !videoPresent || !audioPresent {
			return false
		}
	case youtubeFormatVideo:
		if !videoPresent || audioPresent {
			return false
		}
	case youtubeFormatAudio:
		if videoPresent || !audioPresent {
			return false
		}
	default:
		return false
	}
	if !validYouTubeFormatMetadata(format, videoPresent) {
		return false
	}
	mediaURL, err := url.Parse(format.URL)
	if err != nil || validateYouTubeMediaURL(mediaURL) != nil {
		return false
	}
	if _, _, err := safeYouTubeRequestHeaders(format.HTTPHeaders, expectedID); err != nil {
		return false
	}
	return true
}

func youtubeCodecPresent(codec string) bool {
	codec = strings.ToLower(strings.TrimSpace(codec))
	return codec != "" && codec != "none"
}

func supportedYouTubeSelectionExtension(extension string, role youtubeFormatRole) bool {
	extension = normalizedYouTubeExtension(extension)
	switch role {
	case youtubeFormatAudio:
		return extension == "mp4" || extension == "m4a" || extension == "webm"
	case youtubeFormatCombined, youtubeFormatVideo:
		return extension == "mp4" || extension == "webm"
	default:
		return false
	}
}

func normalizedYouTubeExtension(extension string) string {
	return strings.ToLower(strings.TrimSpace(extension))
}

func compareYouTubeVideoFormats(left, right youtubeFormat) int {
	leftHeight, rightHeight := left.Height, right.Height
	if leftHeight != rightHeight {
		return compareInt(leftHeight, rightHeight)
	}
	if result := comparePixelArea(left, right); result != 0 {
		return result
	}
	if left.Width != right.Width {
		return compareInt(left.Width, right.Width)
	}
	if result := compareNumber(left.FPS, right.FPS); result != 0 {
		return result
	}
	if result := compareNumber(left.Quality, right.Quality); result != 0 {
		return result
	}
	return compareNumber(left.TBR, right.TBR)
}

func compareYouTubeAudioFormats(left, right youtubeFormat) int {
	if result := compareNumber(left.ABR, right.ABR); result != 0 {
		return result
	}
	if result := compareNumber(left.ASR, right.ASR); result != 0 {
		return result
	}
	if result := compareNumber(left.AudioChannels, right.AudioChannels); result != 0 {
		return result
	}
	return compareNumber(left.TBR, right.TBR)
}

func compareInt(left, right int) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func compareNumber(left, right json.Number) int {
	leftValue, _ := parseYouTubeMetadataNumber(left)
	rightValue, _ := parseYouTubeMetadataNumber(right)
	if leftValue < rightValue {
		return -1
	}
	if leftValue > rightValue {
		return 1
	}
	return 0
}

func parseYouTubeMetadataNumber(number json.Number) (float64, bool) {
	if number == "" {
		return 0, true
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// safeYouTubeMetadataNumber is used when copying already-validated selection
// metadata into the durable expectation. Invalid values are filtered before a
// format can be selected.
func safeYouTubeMetadataNumber(number json.Number) float64 {
	value, _ := parseYouTubeMetadataNumber(number)
	return value
}

func youtubeFormatLanguagePreference(number json.Number) int {
	value, ok := parseYouTubeMetadataNumber(number)
	if ok && value == youtubeOriginalLanguagePreference {
		return youtubeOriginalLanguagePreference
	}
	return 0
}

func validYouTubeFormatMetadata(format youtubeFormat, hasVideo bool) bool {
	if format.Width < 0 || format.Height < 0 || hasVideo && (format.Width == 0 || format.Height == 0) {
		return false
	}
	for _, size := range []json.Number{format.FileSize, format.FileSizeApprox} {
		if _, _, err := parseYouTubeDeclaredSize(size); err != nil {
			return false
		}
	}
	for _, number := range []json.Number{
		format.FPS, format.TBR, format.ABR, format.ASR, format.AudioChannels,
	} {
		value, ok := parseYouTubeMetadataNumber(number)
		if !ok || value < 0 {
			return false
		}
	}
	// Quality only ranks formats. yt-dlp uses negative values for unknown or
	// lower-priority quality, so retain any finite value and let it rank below
	// ordinary nonnegative values instead of discarding an otherwise valid URL.
	if _, ok := parseYouTubeMetadataNumber(format.Quality); !ok {
		return false
	}
	preference, ok := parseYouTubeMetadataNumber(format.LanguagePreference)
	if !ok || preference < 0 && preference != -1 && preference != -10 {
		return false
	}
	return true
}

func comparePixelArea(left, right youtubeFormat) int {
	leftArea := new(big.Int).Mul(big.NewInt(int64(left.Width)), big.NewInt(int64(left.Height)))
	rightArea := new(big.Int).Mul(big.NewInt(int64(right.Width)), big.NewInt(int64(right.Height)))
	return leftArea.Cmp(rightArea)
}

func hasYouTubeAudioDubNote(formats []*youtubeFormat) bool {
	for _, format := range formats {
		if strings.Contains(strings.ToLower(format.FormatNote), "dub") {
			return true
		}
	}
	return false
}

func distinctYouTubeAudioLanguages(formats []*youtubeFormat) int {
	languages := make(map[string]struct{})
	for _, format := range formats {
		language := strings.ToLower(strings.TrimSpace(format.Language))
		languages[language] = struct{}{}
	}
	return len(languages)
}
