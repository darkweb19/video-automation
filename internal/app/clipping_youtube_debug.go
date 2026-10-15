package app

import (
    "errors"
    "os/exec"
    "strings"
)

// youtubeDiagnosticStderr keeps only a bounded tail in memory. Raw extractor
// output must never be written to logs: it can contain signed download URLs,
// query strings, titles, cookies, or other sensitive upstream data.
const youtubeDiagnosticStderrLimit = 8 << 10

type youtubeDiagnosticStderr struct {
    tail []byte
}

func (w *youtubeDiagnosticStderr) Write(p []byte) (int, error) {
    n := len(p)
    if n >= youtubeDiagnosticStderrLimit {
        w.tail = append(w.tail[:0], p[n-youtubeDiagnosticStderrLimit:]...)
        return n, nil
    }
    overflow := len(w.tail) + n - youtubeDiagnosticStderrLimit
    if overflow > 0 {
        w.tail = append(w.tail[:0], w.tail[overflow:]...)
    }
    w.tail = append(w.tail, p...)
    return n, nil
}

// Classify extractor failures into a fixed vocabulary; do not emit or persist
// any upstream error text, even when DEBUG logging is enabled.
func classifyYouTubeDownloaderStderr(stderr []byte) string {
    message := strings.ToLower(string(stderr))
    switch {
    case strings.Contains(message, "tunnel connection failed"),
        strings.Contains(message, "proxyerror"),
        strings.Contains(message, "proxy error"),
        strings.Contains(message, "proxy connect"),
        strings.Contains(message, "connect tunnel"):
        return "proxy_failure"
    case strings.Contains(message, "confirm you're not a bot"),
        strings.Contains(message, "sign in to confirm you"),
        strings.Contains(message, "automated requests"):
        return "bot_challenge"
    case strings.Contains(message, "http error 429"), strings.Contains(message, "429 too many requests"):
        return "upstream_rate_limited"
    case strings.Contains(message, "http error 403"), strings.Contains(message, "403 forbidden"):
        return "upstream_http_403"
    case strings.Contains(message, "http error 401"), strings.Contains(message, "401 unauthorized"):
        return "upstream_http_401"
    case strings.Contains(message, "private video"):
        return "private_video"
    case strings.Contains(message, "age-restricted"), strings.Contains(message, "confirm your age"):
        return "age_restricted"
    case strings.Contains(message, "not available in your country"), strings.Contains(message, "not available in your region"):
        return "geo_blocked"
    case strings.Contains(message, "video unavailable"):
        return "video_unavailable"
    case strings.Contains(message, "requested format is not available"),
        strings.Contains(message, "no video formats found"):
        return "format_unavailable"
    case strings.Contains(message, "certificate verify failed"),
        strings.Contains(message, "certificate_verify_failed"),
        strings.Contains(message, "ssl: certificate"):
        return "certificate_failure"
    case strings.Contains(message, "timed out"), strings.Contains(message, "timeout"):
        return "network_timeout"
    case strings.Contains(message, "ffmpeg"), strings.Contains(message, "ffprobe"):
        return "ffmpeg_failure"
    case strings.Contains(message, "javascript"), strings.Contains(message, "js challenge"):
        return "javascript_challenge"
    case len(stderr) == 0:
        return "no_stderr"
    default:
        return "unclassified_extractor_error"
    }
}

func youtubeDownloaderExitCode(err error) int {
    if err == nil {
        return 0
    }
    var exit *exec.ExitError
    if errors.As(err, &exit) {
        return exit.ExitCode()
    }
    return -1
}
