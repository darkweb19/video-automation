package app

import (
	"errors"
	"net/url"
	"strings"
)

var ErrClippingRightsNotAttested = errors.New("explicit rights and processing attestation is required")

const clippingSourceURLSetting = "clipping_source_url"

type ClippingAcquisitionError struct {
	Status  string
	Reason  string
	Message string
}

func (failure *ClippingAcquisitionError) Error() string {
	if failure == nil || failure.Message == "" {
		return "source acquisition failed"
	}
	return failure.Message
}

func clippingOutcome(status, reason, message string) error {
	return &ClippingAcquisitionError{Status: status, Reason: reason, Message: message}
}

func normalizeClippingImportURL(raw string) (*url.URL, ClippingSourceKind, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 8192 || hasURLControl(raw) || strings.Contains(raw, "#") {
		return nil, "", clippingOutcome("unsupported", "invalid_url", "Enter a valid public YouTube video link without credentials or fragments")
	}
	if strings.HasPrefix(strings.ToLower(raw), "http://") {
		return nil, "", clippingOutcome("unsupported", "https_required", "Use an HTTPS public YouTube video link")
	}
	if videoID, ok := flexibleYouTubeVideoID(raw); ok {
		candidate := raw
		if !strings.Contains(candidate, "://") {
			candidate = "https://" + candidate
		}
		parsed, err := url.Parse(candidate)
		if err != nil {
			return nil, "", clippingOutcome("unsupported", "invalid_url", "Enter a valid public YouTube video link")
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil {
			return nil, "", clippingOutcome("unsupported", "invalid_url", "Enter a valid public YouTube video link")
		}
		for key := range query {
			switch strings.ToLower(key) {
			case "list", "playlist", "playlist_id", "video_ids", "start_radio", "index":
				return nil, "", clippingOutcome("unsupported", "unsupported_youtube_url_shape", "Playlists are not supported; enter one YouTube video link")
			}
		}
		return &url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/watch", RawQuery: "v=" + videoID}, ClippingSourceYouTube, nil
	}
	return nil, "", clippingOutcome("unsupported", "unsupported_source_host", "Only public YouTube video links are supported")
}

func hasURLControl(raw string) bool {
	for _, character := range raw {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func flexibleYouTubeVideoID(raw string) (string, bool) {
	if validYouTubeVideoID(raw) {
		return raw, true
	}
	candidate := raw
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	u, err := url.Parse(candidate)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawFragment != "" {
		return "", false
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", false
	}
	if port := u.Port(); port != "" && port != "443" && !(u.Scheme == "http" && port == "80") {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasSuffix(u.Host, ".") || strings.Contains(u.Host, "%") {
		return "", false
	}
	switch host {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com",
		"youtube-nocookie.com", "www.youtube-nocookie.com", "youtu.be", "www.youtu.be":
	default:
		return "", false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", false
	}
	var id string
	if host == "youtu.be" || host == "www.youtu.be" {
		id = strings.Trim(strings.TrimPrefix(u.Path, "/"), "/")
		if strings.Contains(id, "/") {
			return "", false
		}
	} else if u.Path == "/watch" || u.Path == "/watch/" {
		values := query["v"]
		if len(values) != 1 {
			return "", false
		}
		id = values[0]
	} else {
		for _, prefix := range []string{"/shorts/", "/embed/", "/live/", "/v/"} {
			if strings.HasPrefix(u.Path, prefix) {
				id = strings.TrimSuffix(strings.TrimPrefix(u.Path, prefix), "/")
				if strings.Contains(id, "/") {
					return "", false
				}
				break
			}
		}
	}
	return id, validYouTubeVideoID(id)
}

func validYouTubeVideoID(id string) bool {
	if len(id) != 11 {
		return false
	}
	for _, character := range id {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}
