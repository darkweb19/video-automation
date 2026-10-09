package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const youtubeParserFixture = `{"id":"abcdefghijk","duration":75.125,"filesize":1024,"_type":"video","live_status":"not_live","is_live":false,"format_id":"18","ext":"mp4","protocol":"https","url":"https://rr1---sn.googlevideo.com/videoplayback?signature=signed-secret","acodec":"mp4a.40.2","vcodec":"avc1.42001E","requested_downloads":[{"format_id":"18","ext":"mp4","protocol":"https","url":"https://rr1---sn.googlevideo.com/videoplayback?signature=signed-secret","acodec":"mp4a.40.2","vcodec":"avc1.42001E","http_headers":{"User-Agent":"SyntheticRuntimeCheck/1.0","Referer":"https://www.youtube.com/watch?v=abcdefghijk","Accept":"*/*","Accept-Language":"en-US,en;q=0.5","Sec-Fetch-Mode":"navigate","X-Untrusted":"must-not-forward"}}]}`

func TestYouTubeDenoCacheUsesPrivatePermissions(t *testing.T) {
	directory, err := createYouTubeDenoCache()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("Deno cache mode=%#o; want 0700", got)
	}
}

func TestYouTubeExecutableResolutionIgnoresAmbientPATH(t *testing.T) {
	directory := t.TempDir()
	writeFakeYouTubeExecutable(t, directory, "yt-dlp", "exit 0\n")
	t.Setenv("PATH", directory)
	if got := findYouTubeExecutable([]string{filepath.Join(directory, "missing-yt-dlp")}); got != "" {
		t.Fatalf("untrusted PATH executable was selected: %q", got)
	}
	for _, candidate := range clippingYouTubeExecutablePaths {
		if !filepath.IsAbs(candidate) || filepath.Base(candidate) != "yt-dlp" {
			t.Fatalf("production yt-dlp candidate is not a fixed absolute path: %q", candidate)
		}
	}
}

func TestParseYouTubeExtractorOutputAcceptsOneMuxedPinnedShape(t *testing.T) {
	selection, err := parseYouTubeExtractorOutput([]byte(youtubeParserFixture), "abcdefghijk")
	if err != nil {
		t.Fatalf("parse synthetic yt-dlp output: %v", err)
	}
	if selection.DurationMS != 75125 || selection.URL == nil || selection.URL.Host != "rr1---sn.googlevideo.com" || selection.URL.Query().Get("signature") != "signed-secret" {
		t.Fatalf("selected metadata=%+v", selection)
	}
	if selection.UserAgent != "SyntheticRuntimeCheck/1.0" || selection.Referer != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("selected request headers were not safely normalized: %+v", selection)
	}
}

func TestParseYouTubeExtractorOutputRejectsUnsafeAndUnsupportedMetadata(t *testing.T) {
	var base map[string]any
	if err := json.Unmarshal([]byte(youtubeParserFixture), &base); err != nil {
		t.Fatal(err)
	}
	clone := func() map[string]any {
		encoded, _ := json.Marshal(base)
		var copy map[string]any
		_ = json.Unmarshal(encoded, &copy)
		return copy
	}
	cases := []struct {
		name string
		edit func(map[string]any)
		code string
	}{
		{name: "mismatched id", edit: func(value map[string]any) { value["id"] = "lmnopqrstuv" }, code: "youtube_import_unavailable"},
		{name: "playlist object", edit: func(value map[string]any) { value["_type"] = "playlist"; value["entries"] = []any{} }, code: "youtube_format_unavailable"},
		{name: "live flag", edit: func(value map[string]any) { value["is_live"] = true }, code: "youtube_format_unavailable"},
		{name: "upcoming status", edit: func(value map[string]any) { value["live_status"] = "is_upcoming" }, code: "youtube_format_unavailable"},
		{name: "restricted availability", edit: func(value map[string]any) { value["availability"] = "needs_auth" }, code: "youtube_video_unavailable"},
		{name: "unknown availability", edit: func(value map[string]any) { value["availability"] = "unrecognized_state" }, code: "youtube_format_unavailable"},
		{name: "missing duration", edit: func(value map[string]any) { delete(value, "duration") }, code: "youtube_format_unavailable"},
		{name: "invalid duration", edit: func(value map[string]any) { value["duration"] = "NaN" }, code: "youtube_format_unavailable"},
		{name: "duration exceeds four hours", edit: func(value map[string]any) { value["duration"] = MaxClippingSourceDurationMS/1000 + 1 }, code: "youtube_duration_limit_exceeded"},
		{name: "declared size exceeds 20 GiB", edit: func(value map[string]any) { value["filesize"] = MaxClippingSourceBytes + 1 }, code: "youtube_size_limit_exceeded"},
		{name: "declared size exceeds int64", edit: func(value map[string]any) { value["filesize"] = json.Number("999999999999999999999999") }, code: "youtube_size_limit_exceeded"},
		{name: "video only", edit: func(value map[string]any) {
			value["requested_downloads"].([]any)[0].(map[string]any)["acodec"] = "none"
		}, code: "youtube_format_unavailable"},
		{name: "audio only", edit: func(value map[string]any) {
			value["requested_downloads"].([]any)[0].(map[string]any)["vcodec"] = "none"
		}, code: "youtube_format_unavailable"},
		{name: "no selected format", edit: func(value map[string]any) { value["requested_downloads"] = []any{} }, code: "youtube_format_unavailable"},
		{name: "multiple selected formats", edit: func(value map[string]any) {
			value["requested_downloads"] = append(value["requested_downloads"].([]any), value["requested_downloads"].([]any)[0])
		}, code: "youtube_format_unavailable"},
		{name: "selected format lacks direct protocol", edit: func(value map[string]any) {
			delete(value["requested_downloads"].([]any)[0].(map[string]any), "protocol")
		}, code: "youtube_format_unavailable"},
		{name: "manifest protocol", edit: func(value map[string]any) {
			value["requested_downloads"].([]any)[0].(map[string]any)["protocol"] = "http_dash_segments"
		}, code: "youtube_format_unavailable"},
		{name: "unsupported container", edit: func(value map[string]any) {
			value["requested_downloads"].([]any)[0].(map[string]any)["ext"] = "mkv"
		}, code: "youtube_format_unavailable"},
		{name: "invalid user agent", edit: func(value map[string]any) {
			value["requested_downloads"].([]any)[0].(map[string]any)["http_headers"].(map[string]any)["User-Agent"] = "bad\r\nInjected: yes"
		}, code: "youtube_format_unavailable"},
		{name: "oversized user agent", edit: func(value map[string]any) {
			value["requested_downloads"].([]any)[0].(map[string]any)["http_headers"].(map[string]any)["User-Agent"] = strings.Repeat("A", 513)
		}, code: "youtube_format_unavailable"},
		{name: "referer to unapproved host", edit: func(value map[string]any) {
			value["requested_downloads"].([]any)[0].(map[string]any)["http_headers"].(map[string]any)["Referer"] = "https://evil.test/watch?v=abcdefghijk"
		}, code: "youtube_format_unavailable"},
		{name: "referer with extra query", edit: func(value map[string]any) {
			value["requested_downloads"].([]any)[0].(map[string]any)["http_headers"].(map[string]any)["Referer"] = "https://www.youtube.com/watch?v=abcdefghijk&list=private-list"
		}, code: "youtube_format_unavailable"},
		{name: "cookie header rejected", edit: func(value map[string]any) {
			value["requested_downloads"].([]any)[0].(map[string]any)["http_headers"].(map[string]any)["Cookie"] = "private-cookie"
		}, code: "youtube_format_unavailable"},
		{name: "authorization header rejected", edit: func(value map[string]any) {
			value["requested_downloads"].([]any)[0].(map[string]any)["http_headers"].(map[string]any)["Authorization"] = "private-token"
		}, code: "youtube_format_unavailable"},
		{name: "proxy authorization header rejected", edit: func(value map[string]any) {
			value["requested_downloads"].([]any)[0].(map[string]any)["http_headers"].(map[string]any)["Proxy-Authorization"] = "private-token"
		}, code: "youtube_format_unavailable"},
		{name: "host header rejected", edit: func(value map[string]any) {
			value["requested_downloads"].([]any)[0].(map[string]any)["http_headers"].(map[string]any)["Host"] = "attacker.test"
		}, code: "youtube_format_unavailable"},
		{name: "unapproved host", edit: func(value map[string]any) {
			format := value["requested_downloads"].([]any)[0].(map[string]any)
			format["url"] = "https://evil.test/video?signature=signed-secret"
		}, code: "youtube_format_unavailable"},
		{name: "non-HTTPS media", edit: func(value map[string]any) {
			format := value["requested_downloads"].([]any)[0].(map[string]any)
			format["url"] = "http://rr1---sn.googlevideo.com/video?signature=signed-secret"
		}, code: "youtube_format_unavailable"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value := clone()
			test.edit(value)
			encoded, _ := json.Marshal(value)
			_, err := parseYouTubeExtractorOutput(encoded, "abcdefghijk")
			if err == nil || safeClippingFailure(err) != test.code {
				t.Fatalf("error=%v code=%q; want %q", err, safeClippingFailure(err), test.code)
			}
			if strings.Contains(err.Error(), "signed-secret") || strings.Contains(err.Error(), "evil.test") {
				t.Fatalf("untrusted metadata leaked in error: %v", err)
			}
		})
	}
}

func TestYouTubeMediaURLPolicyAndRedirectRedaction(t *testing.T) {
	allowed, err := url.Parse("https://rr1---sn.googlevideo.com/videoplayback?signature=private-token")
	if err != nil || validateYouTubeMediaURL(allowed) != nil {
		t.Fatalf("approved CDN URL rejected: %v", err)
	}
	for _, raw := range []string{
		"http://rr1---sn.googlevideo.com/video",
		"https://googlevideo.com/video",
		"https://googlevideo.com.evil.test/video",
		"https://rr1---sn.googlevideo.com:444/video",
		"https://user@rr1---sn.googlevideo.com/video",
		"https://rr1---sn.googlevideo.com/video#fragment",
	} {
		parsed, parseErr := url.Parse(raw)
		if parseErr != nil {
			t.Fatalf("parse test URL %q: %v", raw, parseErr)
		}
		if validateYouTubeMediaURL(parsed) == nil {
			t.Errorf("unsafe CDN URL accepted: %q", raw)
		}
	}

	_, _, _, acquisition := newClippingAcquisitionFixture(t)
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
	var requestCount int
	acquisition.transportFactory = func(u *url.URL, ips []net.IP) http.RoundTripper {
		return clippingRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			requestCount++
			if len(ips) != 1 || !ips[0].Equal(net.ParseIP("8.8.8.8")) {
				t.Fatalf("media fetch did not receive pinned IPs: %v", ips)
			}
			if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" || request.Header.Get("Proxy-Authorization") != "" {
				t.Fatal("YouTube media request forwarded ambient credentials")
			}
			if request.Header.Get("User-Agent") != "SyntheticRuntimeCheck/1.0" || request.Header.Get("Referer") != "https://www.youtube.com/watch?v=abcdefghijk" {
				t.Fatalf("YouTube media request headers were not safely normalized: %#v", request.Header)
			}
			for _, name := range []string{"Accept-Language", "Sec-Fetch-Mode", "X-Untrusted"} {
				if request.Header.Get(name) != "" {
					t.Fatalf("extractor header %q was forwarded", name)
				}
			}
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://evil.test/redirect?signature=other-private-token"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
	}
	_, err = acquisition.fetchYouTubeMediaURL(context.Background(), youtubeSelection{
		URL:       allowed,
		UserAgent: "SyntheticRuntimeCheck/1.0",
		Referer:   "https://www.youtube.com/watch?v=abcdefghijk",
	})
	if err == nil || requestCount != 1 || strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "other-private-token") || strings.Contains(err.Error(), "evil.test") {
		t.Fatalf("redirect error=%v requests=%d", err, requestCount)
	}
}

func TestYouTubeProxyAuthenticationAllowlistAndPinnedDial(t *testing.T) {
	resolver := &youtubeProxyTestResolver{addresses: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	proxy, err := startYouTubeProxy(context.Background(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	var dialed string
	var dialCalls int
	proxy.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		dialCalls++
		dialed = network + ":" + address
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			buffer := make([]byte, 5)
			if _, err := io.ReadFull(server, buffer); err == nil {
				_, _ = server.Write([]byte("reply"))
			}
		}()
		return client, nil
	}

	status, tunnel, reader := youtubeProxyRequest(t, proxy, "www.youtube.com:443", true, true)
	if !strings.Contains(status, "200") {
		t.Fatalf("allowed CONNECT status %q", status)
	}
	if dialCalls != 1 || dialed != "tcp:8.8.8.8:443" {
		t.Fatalf("proxy dial=%q calls=%d", dialed, dialCalls)
	}
	if hosts := resolver.hosts(); len(hosts) != 1 || hosts[0] != "www.youtube.com" {
		t.Fatalf("DNS hosts=%v", hosts)
	}
	if _, err := tunnel.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 5)
	if _, err := io.ReadFull(reader, response); err != nil || string(response) != "reply" {
		t.Fatalf("tunnel response=%q error=%v", response, err)
	}
	_ = tunnel.Close()

	for _, test := range []struct {
		name      string
		authority string
		auth      bool
		connect   bool
		wantCode  string
	}{
		{name: "no authentication", authority: "www.youtube.com:443", connect: true, wantCode: "407"},
		{name: "unapproved host", authority: "attacker.test:443", auth: true, connect: true, wantCode: "403"},
		{name: "unapproved port", authority: "www.youtube.com:444", auth: true, connect: true, wantCode: "403"},
		{name: "ordinary HTTP rejected", authority: "www.youtube.com:443", auth: true, connect: false, wantCode: "405"},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, connection, _ := youtubeProxyRequest(t, proxy, test.authority, test.auth, test.connect)
			if !strings.Contains(status, test.wantCode) {
				t.Fatalf("status=%q; want HTTP %s", status, test.wantCode)
			}
			if connection != nil {
				_ = connection.Close()
			}
		})
	}
	if dialCalls != 1 {
		t.Fatalf("invalid proxy requests reached upstream dialer: %d", dialCalls-1)
	}
}

func TestYouTubeProxyRejectsMixedPrivateDNSBeforeDial(t *testing.T) {
	resolver := &youtubeProxyTestResolver{addresses: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("192.168.0.2")}}}
	proxy, err := startYouTubeProxy(context.Background(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	var dialCalls int
	proxy.dial = func(context.Context, string, string) (net.Conn, error) {
		dialCalls++
		return nil, errors.New("unexpected dial")
	}
	status, connection, _ := youtubeProxyRequest(t, proxy, "youtube.com:443", true, true)
	if !strings.Contains(status, "502") || connection != nil || dialCalls != 0 {
		t.Fatalf("mixed DNS response=%q connection=%v dials=%d", status, connection, dialCalls)
	}
	if hosts := resolver.hosts(); len(hosts) != 1 || hosts[0] != "youtube.com" {
		t.Fatalf("DNS hosts=%v", hosts)
	}
}

func TestYouTubeProxyTriesNextValidatedAddressWithoutResolvingAgain(t *testing.T) {
	resolver := &youtubeProxyTestResolver{addresses: []net.IPAddr{
		{IP: net.ParseIP("8.8.8.8")},
		{IP: net.ParseIP("1.1.1.1")},
	}}
	proxy, err := startYouTubeProxy(context.Background(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	var mu sync.Mutex
	var dialed []string
	proxy.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, network+":"+address)
		attempt := len(dialed)
		mu.Unlock()
		if attempt == 1 {
			return nil, errors.New("synthetic first address failure")
		}
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			buffer := make([]byte, 5)
			if _, err := io.ReadFull(server, buffer); err == nil {
				_, _ = server.Write([]byte("reply"))
			}
		}()
		return client, nil
	}

	status, tunnel, reader := youtubeProxyRequest(t, proxy, "www.youtube.com:443", true, true)
	if !strings.Contains(status, "200") {
		t.Fatalf("allowed CONNECT status %q", status)
	}
	if _, err := tunnel.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 5)
	if _, err := io.ReadFull(reader, response); err != nil || string(response) != "reply" {
		t.Fatalf("tunnel response=%q error=%v", response, err)
	}
	_ = tunnel.Close()

	mu.Lock()
	gotDialed := append([]string(nil), dialed...)
	mu.Unlock()
	if len(gotDialed) != 2 || gotDialed[0] != "tcp:8.8.8.8:443" || gotDialed[1] != "tcp:1.1.1.1:443" {
		t.Fatalf("dial attempts=%v", gotDialed)
	}
	if hosts := resolver.hosts(); len(hosts) != 1 || hosts[0] != "www.youtube.com" {
		t.Fatalf("DNS was not resolved exactly once: %v", hosts)
	}
}

func TestYouTubeProxyClosesActiveTunnelOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	proxy, err := startYouTubeProxy(ctx, &youtubeProxyTestResolver{addresses: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer proxy.Close()
	defer cancel()

	upstreamReady := make(chan net.Conn, 1)
	proxy.dial = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		upstreamReady <- server
		return client, nil
	}
	status, tunnel, reader := youtubeProxyRequest(t, proxy, "www.youtube.com:443", true, true)
	if !strings.Contains(status, "200") {
		t.Fatalf("allowed CONNECT status %q", status)
	}
	upstream := <-upstreamReady
	cancel()
	_ = tunnel.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("active client tunnel remained open after cancellation")
	}
	_ = upstream.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := upstream.Read(make([]byte, 1)); err == nil {
		t.Fatal("active upstream tunnel remained open after cancellation")
	}
}

func TestYouTubeImportUsesFixedCommandAndKeepsSignedURLOutOfPersistence(t *testing.T) {
	_, store, security, acquisition := newClippingAcquisitionFixture(t)
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
	acquisition.probe = func(context.Context, string) (clippingMediaProbeResult, error) {
		return clippingMediaProbeResult{DurationMS: 75_125, Width: 1280, Height: 720, AudioStreams: 1}, nil
	}
	acquisition.transportFactory = func(u *url.URL, addresses []net.IP) http.RoundTripper {
		return clippingRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if u.Host != "rr1---sn.googlevideo.com" || len(addresses) != 1 || !addresses[0].Equal(net.ParseIP("8.8.8.8")) {
				t.Fatalf("unexpected YouTube fetch destination: URL=%v addresses=%v", u, addresses)
			}
			if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" || request.Header.Get("Proxy-Authorization") != "" {
				t.Fatal("media request forwarded credentials")
			}
			if request.Header.Get("User-Agent") != "SyntheticRuntimeCheck/1.0" || request.Header.Get("Referer") != "https://www.youtube.com/watch?v=abcdefghijk" {
				t.Fatalf("media request headers=%#v", request.Header)
			}
			for _, name := range []string{"Accept-Language", "Sec-Fetch-Mode", "X-Untrusted"} {
				if request.Header.Get(name) != "" {
					t.Fatalf("extractor header %q was forwarded", name)
				}
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"video/mp4"}}, Body: io.NopCloser(bytes.NewReader([]byte("synthetic media bytes"))), ContentLength: 21}, nil
		})
	}

	fixtureDir := t.TempDir()
	infoPath := filepath.Join(fixtureDir, "metadata.json")
	argsPath := filepath.Join(fixtureDir, "args")
	envPath := filepath.Join(fixtureDir, "env")
	if err := os.WriteFile(infoPath, []byte(youtubeParserFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YOUTUBE_IMPORT_TEST_SENTINEL", "must-not-reach-extractor")
	acquisition.youtubeExecutable = writeFakeYouTubeExecutable(t, fixtureDir, "yt-dlp", fmt.Sprintf("printf '%%s\\n%%s\\n%%s\\n' \"$HOME\" \"$DENO_DIR\" \"${YOUTUBE_IMPORT_TEST_SENTINEL-}\" > %q\ntest -d \"$DENO_DIR\"\nprintf writable > \"$DENO_DIR/cache-marker\"\nprintf '%%s\\0' \"$@\" > %q\ncat %q\n", envPath, argsPath, infoPath))
	acquisition.youtubeRuntime = "deno:" + writeFakeYouTubeExecutable(t, fixtureDir, "deno", "exit 0\n")

	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/abcdefghijk?si=tracking", 0)
	if err != nil {
		t.Fatal(err)
	}
	if source.SourceURL == "" || strings.Contains(source.SourceURL, "signed-secret") {
		t.Fatalf("encrypted canonical source URL contains direct media data: %q", source.SourceURL)
	}
	if plaintext, err := security.DecryptSetting(clippingSourceURLSetting, source.SourceURL); err != nil || plaintext != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("stored source URL=%q error=%v", plaintext, err)
	}
	if worked, err := acquisition.ProcessNextImport(context.Background()); err != nil || !worked {
		t.Fatalf("process import worked=%v error=%v", worked, err)
	}
	environment, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	envLines := strings.Split(strings.TrimSuffix(string(environment), "\n"), "\n")
	if len(envLines) != 3 || envLines[0] != "/nonexistent" || envLines[2] != "" || !filepath.IsAbs(envLines[1]) {
		t.Fatalf("extractor environment was not isolated: %q", environment)
	}
	if _, err := os.Stat(envLines[1]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("per-import Deno cache survived successful extraction: %v", err)
	}
	ready, err := store.ClippingSource(source.ID)
	if err != nil || ready.Status != ClippingSourceReady || ready.SizeBytes != 21 {
		t.Fatalf("ready source=%+v error=%v", ready, err)
	}
	media, err := os.ReadFile(ready.StoragePath)
	if err != nil || string(media) != "synthetic media bytes" {
		t.Fatalf("stored bytes=%q error=%v", media, err)
	}
	storedCiphertext, err := store.db.Query(`SELECT source_url FROM clipping_sources WHERE id=?`, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer storedCiphertext.Close()
	if !storedCiphertext.Next() {
		t.Fatal("YouTube source row disappeared")
	}
	var sourceURL string
	if err := storedCiphertext.Scan(&sourceURL); err != nil || strings.Contains(sourceURL, "signed-secret") {
		t.Fatalf("signed direct URL persisted in SQLite: %q error=%v", sourceURL, err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	arguments := strings.Split(strings.TrimSuffix(string(args), "\x00"), "\x00")
	for _, required := range []string{"--ignore-config", "--no-plugin-dirs", "--no-cookies", "--no-playlist", "--skip-download", "--no-simulate", "--dump-single-json", "--no-remote-components", "--js-runtimes", acquisition.youtubeRuntime, "--format", clippingYouTubeMuxedFormat} {
		if !containsString(arguments, required) {
			t.Errorf("fixed yt-dlp args omitted %q: %q", required, safeYouTubeTestArgs(arguments))
		}
	}
	if !containsString(arguments, "https://www.youtube.com/watch?v=abcdefghijk") || containsString(arguments, "si=tracking") || containsString(arguments, "--cookies") || containsString(arguments, "--netrc") {
		t.Fatalf("yt-dlp received query noise, credentials options or noncanonical URL: %q", safeYouTubeTestArgs(arguments))
	}
	for _, argument := range arguments {
		if strings.Contains(argument, "signed-secret") {
			t.Fatal("subprocess argument contains a signed media URL")
		}
	}
}

func TestYouTubeExtractorDenoCacheIsRemovedAfterCommandFailure(t *testing.T) {
	_, _, _, acquisition := newClippingAcquisitionFixture(t)
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
	directory := t.TempDir()
	cachePathFile := filepath.Join(directory, "cache-path")
	acquisition.youtubeExecutable = writeFakeYouTubeExecutable(t, directory, "yt-dlp", fmt.Sprintf("printf '%%s' \"$DENO_DIR\" > %q\ntest -d \"$DENO_DIR\"\nprintf writable > \"$DENO_DIR/cache-marker\"\nexit 17\n", cachePathFile))
	acquisition.youtubeRuntime = "deno:" + writeFakeYouTubeExecutable(t, directory, "deno", "exit 0\n")
	canonicalURL, err := url.Parse("https://www.youtube.com/watch?v=abcdefghijk")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquisition.extractYouTubeMedia(context.Background(), canonicalURL); err == nil {
		t.Fatal("failing extractor unexpectedly succeeded")
	}
	cachePath, err := os.ReadFile(cachePathFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(cachePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("per-import Deno cache survived failed extraction: %v", err)
	}
}

func TestYouTubeExtractorOutputLimitSanitizesAndFailsSource(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
	directory := t.TempDir()
	marker := "extractor-output-secret-marker"
	body := fmt.Sprintf("printf '%%s' %q\nhead -c %d /dev/zero | tr '\\000' x\n", marker, clippingYouTubeOutputLimit+1)
	acquisition.youtubeExecutable = writeFakeYouTubeExecutable(t, directory, "yt-dlp", body)
	acquisition.youtubeRuntime = "deno:" + writeFakeYouTubeExecutable(t, directory, "deno", "exit 0\n")
	canonicalURL, err := url.Parse("https://www.youtube.com/watch?v=abcdefghijk")
	if err != nil {
		t.Fatal(err)
	}
	_, err = acquisition.extractYouTubeMedia(context.Background(), canonicalURL)
	if err == nil || safeClippingFailure(err) != "youtube_format_unavailable" || strings.Contains(err.Error(), marker) {
		t.Fatalf("oversized extractor output error=%v reason=%q", err, safeClippingFailure(err))
	}

	source, err := acquisition.CreatePublicImport(context.Background(), true, canonicalURL.String(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := acquisition.ProcessNextImport(context.Background()); err != nil || !worked {
		t.Fatalf("process oversized extractor output worked=%v error=%v", worked, err)
	}
	failed, err := store.ClippingSource(source.ID)
	if err != nil || failed.Status != ClippingSourceFailed || failed.Failure != "youtube_format_unavailable" {
		t.Fatalf("oversized-output source=%+v error=%v", failed, err)
	}
	if strings.Contains(failed.SourceURL, marker) || strings.Contains(failed.OriginalName, marker) || strings.Contains(failed.Failure, marker) {
		t.Fatalf("extractor output reached source state: %+v", failed)
	}
	rows, err := store.db.Query(`SELECT source_url, original_name, failure FROM clipping_sources WHERE id=?`, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("failed source row disappeared")
	}
	var sourceURL, originalName, failure string
	if err := rows.Scan(&sourceURL, &originalName, &failure); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sourceURL+originalName+failure, marker) {
		t.Fatal("extractor output secret persisted to SQLite")
	}
}

func TestYouTubeActualStreamSizeLimitCleansPartialMedia(t *testing.T) {
	for _, test := range []struct {
		name          string
		contentLength int64
		chunks        [][]byte
	}{
		{name: "declared content length", contentLength: 9, chunks: [][]byte{[]byte("123456789")}},
		{name: "actual streamed bytes after a partial write", contentLength: -1, chunks: [][]byte{[]byte("12345678"), []byte("9")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, store, _, acquisition := newClippingAcquisitionFixture(t)
			acquisition.youtubeMaxBytes = 8
			acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
			acquisition.probe = func(context.Context, string) (clippingMediaProbeResult, error) {
				t.Fatal("oversized media reached ffprobe")
				return clippingMediaProbeResult{}, errClippingInvalidMedia
			}
			acquisition.transportFactory = func(*url.URL, []net.IP) http.RoundTripper {
				return clippingRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode:    http.StatusOK,
						Header:        http.Header{"Content-Type": []string{"video/mp4"}},
						Body:          io.NopCloser(&youtubeChunkReader{chunks: test.chunks}),
						ContentLength: test.contentLength,
					}, nil
				})
			}
			fixtureDir := t.TempDir()
			infoPath := filepath.Join(fixtureDir, "metadata.json")
			if err := os.WriteFile(infoPath, []byte(youtubeParserFixture), 0o600); err != nil {
				t.Fatal(err)
			}
			acquisition.youtubeExecutable = writeFakeYouTubeExecutable(t, fixtureDir, "yt-dlp", fmt.Sprintf("cat %q\n", infoPath))
			acquisition.youtubeRuntime = "deno:" + writeFakeYouTubeExecutable(t, fixtureDir, "deno", "exit 0\n")

			source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/abcdefghijk", 0)
			if err != nil {
				t.Fatal(err)
			}
			if worked, err := acquisition.ProcessNextImport(context.Background()); err != nil || !worked {
				t.Fatalf("process import worked=%v error=%v", worked, err)
			}
			failed, err := store.ClippingSource(source.ID)
			if err != nil || failed.Status != ClippingSourceFailed || failed.Failure != "youtube_size_limit_exceeded" || failed.ReservedSizeBytes != 0 {
				t.Fatalf("oversized YouTube source=%+v error=%v", failed, err)
			}
			paths, err := acquisition.sourcePaths(source.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(paths.temp); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial import file remains: %v", err)
			}
		})
	}
}

func TestYouTubeFinalizationRechecksActualDurationAndAudio(t *testing.T) {
	for _, test := range []struct {
		name       string
		media      clippingMediaProbeResult
		probeErr   error
		wantReason string
	}{
		{name: "actual duration", media: clippingMediaProbeResult{DurationMS: MaxClippingSourceDurationMS + 1, Width: 1280, Height: 720, AudioStreams: 1}, wantReason: "youtube_duration_limit_exceeded"},
		{name: "audio stream duration", probeErr: errors.Join(errClippingInvalidMedia, errClippingDurationLimitExceeded), wantReason: "youtube_duration_limit_exceeded"},
		{name: "missing audio", media: clippingMediaProbeResult{DurationMS: 10_000, Width: 1280, Height: 720}, wantReason: "youtube_media_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, acquisition := newClippingAcquisitionFixture(t)
			source, err := acquisition.CreatePublicImport(context.Background(), true, "https://www.youtube.com/watch?v=abcdefghijk", 0)
			if err != nil {
				t.Fatal(err)
			}
			paths, err := acquisition.sourcePaths(source.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.temp, []byte("synthetic media"), 0o600); err != nil {
				t.Fatal(err)
			}
			acquisition.store.clippingMu.Lock()
			if _, err := acquisition.store.BeginClippingImport(source.ID, 0, time.Minute); err != nil {
				acquisition.store.clippingMu.Unlock()
				t.Fatal(err)
			}
			if err := acquisition.store.UpdateClippingSourceProgress(source.ID, ClippingSourceImporting, 0, int64(len("synthetic media"))); err != nil {
				acquisition.store.clippingMu.Unlock()
				t.Fatal(err)
			}
			acquisition.store.clippingMu.Unlock()
			acquisition.probe = func(context.Context, string) (clippingMediaProbeResult, error) { return test.media, test.probeErr }
			actualErr := func() error {
				_, finalizeErr := acquisition.finalizeSource(context.Background(), source.ID, ClippingSourceImporting)
				return finalizeErr
			}()
			if !errors.Is(actualErr, errClippingInvalidMedia) {
				t.Fatalf("invalid actual probe result error=%v", actualErr)
			}
			if test.wantReason == "youtube_duration_limit_exceeded" && !errors.Is(actualErr, errClippingDurationLimitExceeded) {
				t.Fatalf("actual duration limit error did not preserve its reason: %v", actualErr)
			}
			if got := youtubeProbeFailureReason(actualErr); got != test.wantReason {
				t.Fatalf("probe failure reason=%q want %q", got, test.wantReason)
			}
		})
	}
}

func TestClippingProbeCountsOnlyAudioStreams(t *testing.T) {
	output := clippingProbeOutput{}
	output.Format.Duration = "75"
	output.Format.FormatName = "mp4"
	output.Streams = []clippingProbeStream{
		{CodecType: "video", CodecName: "h264", Width: 1280, Height: 720, Duration: "75"},
		{CodecType: "subtitle", CodecName: "webvtt"},
		{CodecType: "data", CodecName: "timed_id3"},
	}
	decoders := map[string]struct{}{"h264": {}}
	media, err := summarizeClippingProbeStreams(output, decoders)
	if err != nil || media.AudioStreams != 0 {
		t.Fatalf("video+subtitle+data probe=%+v error=%v; want zero audio streams", media, err)
	}

	output.Streams = append(output.Streams, clippingProbeStream{CodecType: "audio", CodecName: "aac", Duration: "75"})
	decoders["aac"] = struct{}{}
	media, err = summarizeClippingProbeStreams(output, decoders)
	if err != nil || media.AudioStreams != 1 {
		t.Fatalf("video+audio probe=%+v error=%v; want one audio stream", media, err)
	}

	output.Streams[len(output.Streams)-1].Duration = fmt.Sprintf("%d", MaxClippingSourceDurationMS/1000+1)
	media, err = summarizeClippingProbeStreams(output, decoders)
	if !errors.Is(err, errClippingInvalidMedia) || !errors.Is(err, errClippingDurationLimitExceeded) {
		t.Fatalf("long audio stream probe=%+v error=%v; want a duration-limit failure", media, err)
	}
}

type youtubeChunkReader struct {
	chunks [][]byte
}

func (reader *youtubeChunkReader) Read(buffer []byte) (int, error) {
	if len(reader.chunks) == 0 {
		return 0, io.EOF
	}
	count := copy(buffer, reader.chunks[0])
	if count == len(reader.chunks[0]) {
		reader.chunks = reader.chunks[1:]
	} else {
		reader.chunks[0] = reader.chunks[0][count:]
	}
	return count, nil
}

func writeFakeYouTubeExecutable(t *testing.T, directory, name, body string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func safeYouTubeTestArgs(arguments []string) []string {
	copy := append([]string(nil), arguments...)
	for index := 0; index+1 < len(copy); index++ {
		if copy[index] != "--proxy" {
			continue
		}
		proxyURL, err := url.Parse(copy[index+1])
		if err == nil && proxyURL.User != nil {
			proxyURL.User = url.User("redacted")
			copy[index+1] = proxyURL.String()
		}
	}
	return copy
}

type youtubeProxyTestResolver struct {
	mu        sync.Mutex
	addresses []net.IPAddr
	calls     []string
}

func (resolver *youtubeProxyTestResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.calls = append(resolver.calls, host)
	return append([]net.IPAddr(nil), resolver.addresses...), nil
}

func (resolver *youtubeProxyTestResolver) hosts() []string {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return append([]string(nil), resolver.calls...)
}

func youtubeProxyRequest(t *testing.T, proxy *youtubeProxy, authority string, authenticated, connect bool) (string, net.Conn, *bufio.Reader) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", proxy.address, time.Second)
	if err != nil {
		t.Fatalf("connect to ephemeral proxy: %v", err)
	}
	method := http.MethodGet
	path := "https://" + authority + "/"
	if connect {
		method = http.MethodConnect
		path = authority
	}
	_, _ = fmt.Fprintf(connection, "%s %s HTTP/1.1\r\nHost: %s\r\n", method, path, authority)
	if authenticated {
		credentials := base64.StdEncoding.EncodeToString([]byte(proxy.username + ":" + proxy.password))
		_, _ = fmt.Fprintf(connection, "Proxy-Authorization: Basic %s\r\n", credentials)
	}
	_, _ = io.WriteString(connection, "\r\n")
	reader := bufio.NewReader(connection)
	status, err := reader.ReadString('\n')
	if err != nil {
		_ = connection.Close()
		t.Fatalf("read proxy response: %v", err)
	}
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			_ = connection.Close()
			t.Fatalf("read proxy response headers: %v", readErr)
		}
		if line == "\r\n" {
			break
		}
	}
	if !connect || !strings.Contains(status, "200") {
		_ = connection.Close()
		return status, nil, nil
	}
	return status, connection, reader
}
