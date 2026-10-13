package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const c25SyntheticVideoID = "abcdefghijk"

type c25SyntheticTracks struct {
	videoPath   string
	audioPath   string
	combinedMP4 string
	mergedMKV   string
	video       []byte
	audio       []byte
	combined    []byte
	merged      []byte
}

func c25MakeSyntheticTracks(t *testing.T) c25SyntheticTracks {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required for the local stream-copy integration fixture")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is required for the local stream-copy integration fixture")
	}
	directory := t.TempDir()
	tracks := c25SyntheticTracks{
		videoPath:   filepath.Join(directory, "video.mp4"),
		audioPath:   filepath.Join(directory, "audio.m4a"),
		combinedMP4: filepath.Join(directory, "combined.mp4"),
		mergedMKV:   filepath.Join(directory, "merged.mkv"),
	}
	c25RunFixtureCommand(t, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=12",
		"-t", "1", "-an", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-movflags", "+faststart", tracks.videoPath)
	c25RunFixtureCommand(t, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "1", "-vn", "-c:a", "aac", "-b:a", "64k", tracks.audioPath)
	c25RunFixtureCommand(t, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", tracks.videoPath, "-i", tracks.audioPath,
		"-map", "0:v:0", "-map", "1:a:0", "-c", "copy", "-movflags", "+faststart", tracks.combinedMP4)
	c25RunFixtureCommand(t, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", tracks.videoPath, "-i", tracks.audioPath,
		"-map", "0:v:0", "-map", "1:a:0", "-c", "copy", "-f", "matroska", tracks.mergedMKV)
	for path, target := range map[string]*[]byte{
		tracks.videoPath:   &tracks.video,
		tracks.audioPath:   &tracks.audio,
		tracks.combinedMP4: &tracks.combined,
		tracks.mergedMKV:   &tracks.merged,
	} {
		*target, err = os.ReadFile(path)
		if err != nil {
			t.Fatalf("read generated fixture %s: %v", filepath.Base(path), err)
		}
	}
	return tracks
}

func c25RunFixtureCommand(t *testing.T, executable string, arguments ...string) {
	t.Helper()
	command := exec.Command(executable, arguments...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate local synthetic media with %s: %v (%s)", filepath.Base(executable), err, strings.TrimSpace(string(output)))
	}
}

func c25InstallExtractor(t *testing.T, acquisition *ClippingAcquisition, metadata []byte, runLog string, beforeOutput string) {
	t.Helper()
	directory := t.TempDir()
	metadataPath := filepath.Join(directory, "synthetic-info.json")
	if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	if beforeOutput != "" {
		body.WriteString(beforeOutput)
		body.WriteByte('\n')
	}
	if runLog != "" {
		body.WriteString(fmt.Sprintf("printf x >> %q\n", runLog))
	}
	body.WriteString(fmt.Sprintf("cat %q\n", metadataPath))
	acquisition.youtubeExecutable = writeFakeYouTubeExecutable(t, directory, "yt-dlp", body.String())
	acquisition.youtubeRuntime = "deno:" + writeFakeYouTubeExecutable(t, directory, "deno", "exit 0\n")
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
}

func c25SeparateMetadata(tracks c25SyntheticTracks, includeDeclaredSizes bool) []byte {
	video := map[string]any{
		"format_id": "video-from-the-catalog", "ext": "mp4", "protocol": "https",
		"url":    "https://rr1---sn.googlevideo.com/video-track?signature=video-secret",
		"vcodec": "avc1.42001E", "acodec": "none", "width": 320, "height": 240,
		"fps": 12, "quality": 4, "tbr": 900,
	}
	originalAudio := map[string]any{
		"format_id": "audio-original-from-the-catalog", "ext": "m4a", "protocol": "https",
		"url":    "https://rr1---sn.googlevideo.com/original-audio?signature=audio-secret",
		"vcodec": "none", "acodec": "mp4a.40.2", "abr": 64, "asr": 48000,
		"audio_channels": 1, "language": "en", "language_preference": 10, "format_note": "Original",
	}
	dubAudio := map[string]any{
		"format_id": "audio-alternate-from-the-catalog", "ext": "m4a", "protocol": "https",
		"url":    "https://rr1---sn.googlevideo.com/alternate-audio?signature=dub-secret",
		"vcodec": "none", "acodec": "mp4a.40.2", "abr": 128, "asr": 48000,
		"audio_channels": 2, "language": "de", "language_preference": -1, "format_note": "German",
	}
	if includeDeclaredSizes {
		video["filesize"] = len(tracks.video)
		originalAudio["filesize"] = len(tracks.audio)
		dubAudio["filesize"] = len(tracks.audio)
	}
	return c25MarshalMetadata(map[string]any{
		"id": c25SyntheticVideoID, "duration": 1.0, "_type": "video", "live_status": "not_live", "is_live": false,
		"formats": []any{video, dubAudio, originalAudio},
	})
}

func c25CombinedMetadata(tracks c25SyntheticTracks) []byte {
	combined := map[string]any{
		"format_id": "progressive-from-the-catalog", "ext": "mp4", "protocol": "https",
		"url":    "https://rr1---sn.googlevideo.com/combined?signature=combined-secret",
		"vcodec": "avc1.42001E", "acodec": "mp4a.40.2", "width": 320, "height": 240,
		"fps": 12, "quality": 4, "tbr": 964, "filesize": len(tracks.combined),
	}
	return c25MarshalMetadata(map[string]any{
		"id": c25SyntheticVideoID, "duration": 1.0, "_type": "video", "live_status": "not_live", "is_live": false,
		"formats": []any{combined},
	})
}

func c25MarshalMetadata(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func c25InstallMediaTransport(acquisition *ClippingAcquisition, tracks c25SyntheticTracks, combined bool, beforeResponse func(*http.Request) (*http.Response, error)) {
	acquisition.transportFactory = func(_ *url.URL, addresses []net.IP) http.RoundTripper {
		if len(addresses) != 1 || !addresses[0].Equal(net.ParseIP("8.8.8.8")) {
			return c25RoundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("synthetic media transport did not receive the pinned public address")
			})
		}
		return c25RoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" || request.Header.Get("Proxy-Authorization") != "" {
				return nil, errors.New("synthetic media request forwarded credentials")
			}
			if beforeResponse != nil {
				if response, err := beforeResponse(request); response != nil || err != nil {
					return response, err
				}
			}
			var media []byte
			contentType := ""
			switch request.URL.Path {
			case "/video-track":
				media, contentType = tracks.video, "video/mp4"
			case "/original-audio", "/alternate-audio":
				media, contentType = tracks.audio, "audio/mp4"
			case "/combined":
				if !combined {
					return nil, errors.New("combined stream was requested during separate-track import")
				}
				media, contentType = tracks.combined, "video/mp4"
			default:
				return nil, fmt.Errorf("unexpected synthetic media path %q", request.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{contentType}},
				Body:       io.NopCloser(bytes.NewReader(media)), ContentLength: int64(len(media)),
			}, nil
		})
	}
}

type c25RoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip c25RoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func c25InstallCountingFFmpeg(t *testing.T, acquisition *ClippingAcquisition, logPath string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required for the local stream-copy integration fixture")
	}
	acquisition.youtubeFFmpegExecutable = writeFakeYouTubeExecutable(t, t.TempDir(), "ffmpeg-copy-wrapper", fmt.Sprintf("printf '%%s\\0' \"$@\" > %q\nexec %q \"$@\"\n", logPath, ffmpeg))
}

type c25PacketCounts struct {
	Streams []struct {
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		ReadPackets string `json:"nb_read_packets"`
	} `json:"streams"`
}

type c25PacketDataHashes struct {
	Packets []struct {
		DataHash string `json:"data_hash"`
	} `json:"packets"`
}

func c25ReadPacketCounts(t *testing.T, path string) c25PacketCounts {
	t.Helper()
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is required for the local stream-copy integration fixture")
	}
	command := exec.Command(ffprobe, "-v", "error", "-count_packets", "-show_entries", "stream=codec_type,codec_name,width,height,nb_read_packets", "-of", "json", path)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("ffprobe packet counts for %s: %v", filepath.Base(path), err)
	}
	var result c25PacketCounts
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("parse ffprobe packet counts for %s: %v", filepath.Base(path), err)
	}
	return result
}

func c25ReadPacketDataHashes(t *testing.T, path, stream string) []string {
	t.Helper()
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is required for the local stream-copy integration fixture")
	}
	command := exec.Command(ffprobe, "-v", "error", "-select_streams", stream, "-show_packets", "-show_data_hash", "sha256", "-show_entries", "packet=data_hash", "-of", "json", path)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("ffprobe packet payload hashes for %s stream %s: %v", filepath.Base(path), stream, err)
	}
	var result c25PacketDataHashes
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("parse ffprobe packet payload hashes for %s stream %s: %v", filepath.Base(path), stream, err)
	}
	if len(result.Packets) == 0 {
		t.Fatalf("ffprobe returned no packet payload hashes for %s stream %s", filepath.Base(path), stream)
	}
	hashes := make([]string, len(result.Packets))
	for index, packet := range result.Packets {
		if packet.DataHash == "" {
			t.Fatalf("ffprobe omitted packet payload hash %d for %s stream %s", index, filepath.Base(path), stream)
		}
		hashes[index] = packet.DataHash
	}
	return hashes
}

func c25FindStream(t *testing.T, streams []struct {
	CodecType   string `json:"codec_type"`
	CodecName   string `json:"codec_name"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	ReadPackets string `json:"nb_read_packets"`
}, codecType string) struct {
	CodecType   string `json:"codec_type"`
	CodecName   string `json:"codec_name"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	ReadPackets string `json:"nb_read_packets"`
} {
	t.Helper()
	for _, stream := range streams {
		if stream.CodecType == codecType {
			return stream
		}
	}
	t.Fatalf("ffprobe result is missing %s stream: %+v", codecType, streams)
	return struct {
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		ReadPackets string `json:"nb_read_packets"`
	}{}
}

func TestYouTubeImportStreamCopiesSeparateTracksAndPreservesProgressivePath(t *testing.T) {
	tracks := c25MakeSyntheticTracks(t)
	for _, test := range []struct {
		name     string
		metadata []byte
		separate bool
	}{
		{name: "separate video and original audio", metadata: c25SeparateMetadata(tracks, true), separate: true},
		{name: "existing combined progressive stream", metadata: c25CombinedMetadata(tracks), separate: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, store, _, acquisition := newClippingAcquisitionFixture(t)
			logPath := filepath.Join(t.TempDir(), "ffmpeg-arguments")
			c25InstallCountingFFmpeg(t, acquisition, logPath)
			c25InstallExtractor(t, acquisition, test.metadata, "", "")
			var mu sync.Mutex
			var requested []string
			c25InstallMediaTransport(acquisition, tracks, !test.separate, func(request *http.Request) (*http.Response, error) {
				mu.Lock()
				requested = append(requested, request.URL.Path)
				mu.Unlock()
				return nil, nil
			})
			source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID+"?si=synthetic-share-token", 0)
			if err != nil {
				t.Fatal(err)
			}
			worked, err := acquisition.ProcessNextImport(context.Background())
			if err != nil || !worked {
				t.Fatalf("ProcessNextImport() worked=%v error=%v", worked, err)
			}
			ready, err := store.ClippingSource(source.ID)
			if err != nil || ready.Status != ClippingSourceReady {
				t.Fatalf("source did not become Ready: %+v error=%v", ready, err)
			}
			if ready.SizeBytes <= 0 || ready.SizeBytes > MaxClippingSourceBytes || ready.SHA256 == "" || ready.UploadOffset != ready.SizeBytes {
				t.Fatalf("Ready source lacks bounded size, hash, or final offset: %+v", ready)
			}
			stored, err := os.ReadFile(ready.StoragePath)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(stored)
			if ready.SHA256 != hex.EncodeToString(digest[:]) || int64(len(stored)) != ready.SizeBytes {
				t.Fatalf("persisted Ready size/hash do not match atomically published bytes: size=%d row=%d hash=%s rowhash=%s", len(stored), ready.SizeBytes, hex.EncodeToString(digest[:]), ready.SHA256)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(ready.StoragePath), "source.video.part")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("video track part survived Ready publication: %v", err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(ready.StoragePath), "source.audio.part")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("audio track part survived Ready publication: %v", err)
			}
			store.clippingMu.Lock()
			_, claimRemains := store.clippingImportWorkspaces[source.ID]
			store.clippingMu.Unlock()
			if claimRemains {
				t.Fatal("track workspace reservation remained after source became Ready")
			}
			acquisition.store.clippingMu.Lock()
			capacityErr := acquisition.checkClippingStorageCapacity(0)
			_, claimWasRecreated := store.clippingImportWorkspaces[source.ID]
			acquisition.store.clippingMu.Unlock()
			if capacityErr != nil || claimWasRecreated {
				t.Fatalf("capacity reconciliation failed or recreated a completed workspace claim: error=%v recreated=%v", capacityErr, claimWasRecreated)
			}

			mu.Lock()
			gotRequests := append([]string(nil), requested...)
			mu.Unlock()
			if test.separate {
				requestCounts := map[string]int{}
				for _, requestPath := range gotRequests {
					requestCounts[requestPath]++
				}
				if len(gotRequests) != 2 || requestCounts["/video-track"] != 1 || requestCounts["/original-audio"] != 1 {
					t.Fatalf("separate stream requests=%v; want one video track and the marked original audio request", gotRequests)
				}
				argumentData, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatal(err)
				}
				arguments := strings.Split(strings.TrimSuffix(string(argumentData), "\x00"), "\x00")
				paths, err := acquisition.sourcePaths(source.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !c25HasPair(arguments, "-c", "copy") || !c25HasPair(arguments, "-protocol_whitelist", "file,pipe") ||
					!c25HasPair(arguments, "-f", "matroska") || len(arguments) == 0 || arguments[len(arguments)-1] != paths.temp || c25Has(arguments, "pipe:1") {
					t.Fatalf("FFmpeg must stream-copy Matroska to the generated seekable output file: %v", arguments)
				}
				if c25Has(arguments, "libx264") || c25Has(arguments, "aac") || c25Has(arguments, "-c:v") || c25Has(arguments, "-c:a") {
					t.Fatalf("FFmpeg command attempted to re-encode separate tracks: %v", arguments)
				}
				inputs := c25ValuesAfter(arguments, "-i")
				if len(inputs) != 2 || inputs[0] != filepath.Join(filepath.Dir(ready.StoragePath), "source.video.part") || inputs[1] != filepath.Join(filepath.Dir(ready.StoragePath), "source.audio.part") {
					t.Fatalf("FFmpeg inputs were not generated local track files: %v", inputs)
				}
				videoBefore := c25FindStream(t, c25ReadPacketCounts(t, tracks.videoPath).Streams, "video")
				audioBefore := c25FindStream(t, c25ReadPacketCounts(t, tracks.audioPath).Streams, "audio")
				finalCounts := c25ReadPacketCounts(t, ready.StoragePath)
				videoAfter := c25FindStream(t, finalCounts.Streams, "video")
				audioAfter := c25FindStream(t, finalCounts.Streams, "audio")
				if videoAfter.CodecName != videoBefore.CodecName || audioAfter.CodecName != audioBefore.CodecName || videoAfter.Width != 320 || videoAfter.Height != 240 || videoAfter.ReadPackets != videoBefore.ReadPackets || audioAfter.ReadPackets != audioBefore.ReadPackets {
					t.Fatalf("stream-copy mux changed codec, dimensions, or packet counts: video %s/%s %dx%d packets %s/%s; audio %s/%s packets %s/%s", videoBefore.CodecName, videoAfter.CodecName, videoAfter.Width, videoAfter.Height, videoBefore.ReadPackets, videoAfter.ReadPackets, audioBefore.CodecName, audioAfter.CodecName, audioBefore.ReadPackets, audioAfter.ReadPackets)
				}
				for _, streams := range []struct{ name, selector, input string }{
					{name: "video", selector: "v:0", input: tracks.videoPath},
					{name: "audio", selector: "a:0", input: tracks.audioPath},
				} {
					before := c25ReadPacketDataHashes(t, streams.input, streams.selector)
					after := c25ReadPacketDataHashes(t, ready.StoragePath, streams.selector)
					if len(before) != len(after) {
						t.Fatalf("stream-copy mux changed %s packet payload count: source=%d output=%d", streams.name, len(before), len(after))
					}
					for index := range before {
						if after[index] != before[index] {
							t.Fatalf("stream-copy mux changed %s packet %d payload hash: source=%s output=%s", streams.name, index, before[index], after[index])
						}
					}
				}
				media, err := probeClippingMedia(context.Background(), ready.StoragePath)
				if err != nil || media.AudioStreams != 1 || media.VideoCodec != "h264" || media.AudioCodec != "aac" || !media.VideoTimingValid || !media.AudioTimingValid || media.VideoDurationMS <= 0 || media.AudioDurationMS <= 0 || c25AbsInt64(media.VideoStartMS-media.AudioStartMS) > clippingYouTubeMaxAVSkewMS {
					t.Fatalf("ffprobe did not return acceptable A/V timing and stream metadata for Matroska output: %+v error=%v", media, err)
				}
				if media.DurationMS < 900 || media.DurationMS > 2_000 || c25AbsInt64(media.VideoDurationMS-media.AudioDurationMS) > clippingYouTubeMaxAVSkewMS {
					t.Fatalf("ffprobe timing disagrees with the one-second source or between tracks: %+v", media)
				}
				if ready.ImportExpectation == "" || strings.Contains(ready.ImportExpectation, "signature=") || strings.Contains(ready.ImportExpectation, "video-from-the-catalog") || strings.Contains(ready.ImportExpectation, "audio-original-from-the-catalog") || strings.Contains(ready.ImportExpectation, "googlevideo.com") {
					t.Fatalf("persisted YouTube expectation is absent or contains ephemeral extractor data: %q", ready.ImportExpectation)
				}
			} else {
				if len(gotRequests) != 1 || gotRequests[0] != "/combined" {
					t.Fatalf("progressive stream requests=%v; want the existing combined source path", gotRequests)
				}
				if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("combined progressive import unexpectedly invoked the separate-track mux: %v", err)
				}
			}
		})
	}
}

func c25AbsInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func c25Has(arguments []string, value string) bool {
	for _, argument := range arguments {
		if argument == value {
			return true
		}
	}
	return false
}

func c25HasPair(arguments []string, key, value string) bool {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == key && arguments[index+1] == value {
			return true
		}
	}
	return false
}

func c25ValuesAfter(arguments []string, key string) []string {
	var values []string
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == key {
			values = append(values, arguments[index+1])
		}
	}
	return values
}

func TestYouTubeSeparateTrackAggregateAndMergedOutputLimitsAreIndependent(t *testing.T) {
	t.Run("aggregate streamed tracks stop before exceeding the injected limit", func(t *testing.T) {
		_, store, _, acquisition := newClippingAcquisitionFixture(t)
		acquisition.youtubeMaxBytes = 8
		metadata := c25TinySeparateMetadata("https://rr1---sn.googlevideo.com/video-track?signature=v", "https://rr1---sn.googlevideo.com/original-audio?signature=a", 0, 0)
		c25InstallExtractor(t, acquisition, metadata, "", "")
		var trackRequests int
		var requestsMu sync.Mutex
		acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
		acquisition.transportFactory = func(*url.URL, []net.IP) http.RoundTripper {
			return c25RoundTripFunc(func(request *http.Request) (*http.Response, error) {
				requestsMu.Lock()
				trackRequests++
				requestsMu.Unlock()
				data, contentType := []byte("12345"), "video/mp4"
				if request.URL.Path == "/original-audio" {
					contentType = "audio/mp4"
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data))}, nil
			})
		}
		if _, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0); err != nil {
			t.Fatal(err)
		}
		worked, err := acquisition.ProcessNextImport(context.Background())
		if err != nil || !worked {
			t.Fatalf("bounded track import worked=%v error=%v", worked, err)
		}
		sources, err := store.ClippingSources()
		if err != nil || len(sources) != 1 || sources[0].Status != ClippingSourceFailed {
			t.Fatalf("aggregate oversize track pair did not fail closed: sources=%+v error=%v", sources, err)
		}
		requestsMu.Lock()
		gotTrackRequests := trackRequests
		requestsMu.Unlock()
		if gotTrackRequests != 2 {
			t.Fatalf("aggregate size check read an unexpected number of track requests: %d", gotTrackRequests)
		}
		if sources[0].SizeBytes > acquisition.youtubeMaxBytes || sources[0].ReservedSizeBytes != 0 {
			t.Fatalf("aggregate track failure exceeded its byte cap or retained source reservation: %+v", sources[0])
		}
		if _, err := os.Stat(filepath.Dir(sources[0].StoragePath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("aggregate track failure left staged files behind: %v", err)
		}
	})

	t.Run("merged seekable file is capped by the subprocess byte limit", func(t *testing.T) {
		tracks := c25MakeSyntheticTracks(t)
		_, _, _, acquisition := newClippingAcquisitionFixture(t)
		snapshot := filepath.Join(t.TempDir(), "capped-output.snapshot")
		ffmpeg, err := exec.LookPath("ffmpeg")
		if err != nil {
			t.Skip("ffmpeg is required for the local stream-copy integration fixture")
		}
		acquisition.youtubeFFmpegExecutable = writeFakeYouTubeExecutable(t, t.TempDir(), "ffmpeg-cap-wrapper", fmt.Sprintf("output=''\nfor arg do output=$arg; done\nln \"$output\" %q\nexec %q \"$@\"\n", snapshot, ffmpeg))
		outputPath := filepath.Join(t.TempDir(), "merged.mkv")
		if err := os.WriteFile(outputPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		err = acquisition.muxYouTubeTracks(context.Background(), "synthetic-source", tracks.videoPath, tracks.audioPath, outputPath, 8)
		if err == nil {
			t.Fatal("FFmpeg succeeded after its seekable output reached the injected hard byte cap")
		}
		if code := clippingDiagnosticCode(ClippingDiagnosticStageTrackMux, err); code != "mux_output_size_limit_exceeded" {
			t.Fatalf("hard output cap was not reported with its bounded size diagnostic: %q error=%v", code, err)
		}
		capped, err := os.Stat(snapshot)
		if err != nil {
			t.Fatalf("read hard-capped output inode after importer cleanup: %v", err)
		}
		if capped.Size() != 8 {
			t.Fatalf("filesystem byte limit did not stop the oversized mux write at the exact injected cap: got=%d cap=%d", capped.Size(), 8)
		}
		if err := os.Remove(snapshot); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed bounded mux retained its partial output file: %v", err)
		}
	})
}

func c25TinySeparateMetadata(videoURL, audioURL string, videoSize, audioSize int) []byte {
	video := map[string]any{
		"format_id": "video-id-is-metadata-only", "ext": "mp4", "protocol": "https", "url": videoURL,
		"vcodec": "avc1.42001E", "acodec": "none", "width": 320, "height": 240,
		"fps": 12, "quality": 4, "tbr": 900,
	}
	audio := map[string]any{
		"format_id": "audio-id-is-metadata-only", "ext": "m4a", "protocol": "https", "url": audioURL,
		"vcodec": "none", "acodec": "mp4a.40.2", "abr": 64, "asr": 48000,
		"audio_channels": 1, "language": "en",
	}
	if videoSize > 0 {
		video["filesize"] = videoSize
	}
	if audioSize > 0 {
		audio["filesize"] = audioSize
	}
	return c25MarshalMetadata(map[string]any{
		"id": c25SyntheticVideoID, "duration": 1.0, "_type": "video", "live_status": "not_live", "is_live": false,
		"formats": []any{video, audio},
	})
}

func TestYouTubeWorkspaceReservationIsSharedAndCleanupRetainsFailedClaim(t *testing.T) {
	_, store, security, first := newClippingAcquisitionFixture(t)
	second := NewClippingAcquisition(store, security)
	second.diskAvailable = func(string) (int64, error) { return 1 << 40, nil }
	source, err := first.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginClippingImport(source.ID, 0, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.SetClippingSourceImportExpectation(source.ID, c25ValidExpectation()); err != nil {
		t.Fatal(err)
	}
	store.clippingMu.Lock()
	if err := first.reserveYouTubeWorkspaceLocked(source.ID); err != nil {
		store.clippingMu.Unlock()
		t.Fatal(err)
	}
	store.clippingMu.Unlock()
	paths, err := first.sourcePaths(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.videoTrack, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := first.cleanupYouTubeTracks(source.ID, paths); err == nil {
		t.Fatal("cleanup unexpectedly removed a non-file track path")
	}
	store.clippingMu.Lock()
	remaining, claimRetained := store.clippingImportWorkspaces[source.ID]
	store.clippingMu.Unlock()
	if !claimRetained || remaining != clippingYouTubeTrackWorkspaceBytes {
		t.Fatalf("failed track cleanup released its workspace claim: retained=%v remaining=%d", claimRetained, remaining)
	}
	if err := os.Remove(paths.videoTrack); err != nil {
		t.Fatal(err)
	}
	if err := first.cleanupYouTubeTracks(source.ID, paths); err != nil {
		t.Fatal(err)
	}
	store.clippingMu.Lock()
	_, claimAfterCleanup := store.clippingImportWorkspaces[source.ID]
	capacityErr := second.checkClippingStorageCapacity(0)
	_, claimAfterReconcile := store.clippingImportWorkspaces[source.ID]
	store.clippingMu.Unlock()
	if capacityErr != nil || claimAfterCleanup || claimAfterReconcile {
		t.Fatalf("cleaned scratch claim was retained or recreated across acquisition instances: err=%v after-clean=%v after-reconcile=%v", capacityErr, claimAfterCleanup, claimAfterReconcile)
	}
}

func TestYouTubeScratchAndFinalReservationsSerializeConcurrentIntake(t *testing.T) {
	_, store, security, first := newClippingAcquisitionFixture(t)
	second := NewClippingAcquisition(store, security)
	available := clippingFreeSpaceFloorBytes + 3*MaxClippingSourceBytes
	first.diskAvailable = func(string) (int64, error) { return available, nil }
	second.diskAvailable = func(string) (int64, error) { return available, nil }
	source, err := first.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
	if err != nil {
		t.Fatal(err)
	}
	store.clippingMu.Lock()
	err = first.reserveYouTubeWorkspaceLocked(source.ID)
	store.clippingMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, acquisition := range []*ClippingAcquisition{first, second} {
		acquisition := acquisition
		go func() {
			<-start
			_, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
			results <- err
		}()
	}
	close(start)
	var accepted, rejected int
	for index := 0; index < 2; index++ {
		err := <-results
		if err == nil {
			accepted++
		} else {
			rejected++
			var acquisitionError *ClippingAcquisitionError
			if !errors.As(err, &acquisitionError) || acquisitionError.Reason != "storage_reservation_unavailable" {
				t.Fatalf("concurrent intake failed for an unexpected reason: %v", err)
			}
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("scratch plus final disk reservations did not serialize intake: accepted=%d rejected=%d", accepted, rejected)
	}
	store.clippingMu.Lock()
	workspaceCount := len(store.clippingImportWorkspaces)
	store.clippingMu.Unlock()
	if workspaceCount != 1 {
		t.Fatalf("shared store lost or duplicated the live scratch reservation: %d claims", workspaceCount)
	}
}

type c25BlockingBody struct {
	ctx     context.Context
	started chan struct{}
	once    sync.Once
}

func (body *c25BlockingBody) Read([]byte) (int, error) {
	body.once.Do(func() { close(body.started) })
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (body *c25BlockingBody) Close() error { return nil }

func TestYouTubeSeparateTrackCancellationCleansFirstOrSecondDownload(t *testing.T) {
	for _, blockedPath := range []string{"/video-track", "/original-audio"} {
		t.Run(strings.TrimPrefix(blockedPath, "/"), func(t *testing.T) {
			_, store, security, acquisition := newClippingAcquisitionFixture(t)
			metadata := c25TinySeparateMetadata("https://rr1---sn.googlevideo.com/video-track?signature=v", "https://rr1---sn.googlevideo.com/original-audio?signature=a", 0, 0)
			c25InstallExtractor(t, acquisition, metadata, "", "")
			acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
			started := make(chan struct{})
			acquisition.transportFactory = func(*url.URL, []net.IP) http.RoundTripper {
				return c25RoundTripFunc(func(request *http.Request) (*http.Response, error) {
					if request.URL.Path == blockedPath {
						body := &c25BlockingBody{ctx: request.Context(), started: started}
						contentType := "video/mp4"
						if blockedPath == "/original-audio" {
							contentType = "audio/mp4"
						}
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: body, ContentLength: -1}, nil
					}
					contentType := "video/mp4"
					if request.URL.Path == "/original-audio" {
						contentType = "audio/mp4"
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader("VIDEO")), ContentLength: 5}, nil
				})
			}
			source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
			if err != nil {
				t.Fatal(err)
			}
			processed := make(chan error, 1)
			go func() {
				_, err := acquisition.ProcessNextImport(context.Background())
				processed <- err
			}()
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("track request did not reach the blocking synthetic response")
			}
			canceler := NewClippingAcquisition(store, security)
			canceler.diskAvailable = func(string) (int64, error) { return 1 << 40, nil }
			if err := canceler.CancelSource(context.Background(), source.ID); err != nil {
				t.Fatalf("cancel source while %s was blocked: %v", blockedPath, err)
			}
			select {
			case err := <-processed:
				if err != nil {
					t.Fatalf("canceled ProcessNextImport returned %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("canceled track subprocess/request did not stop")
			}
			deleted, err := store.ClippingSource(source.ID)
			if err != nil || deleted.Status != ClippingSourceDeleted || !deleted.CleanupComplete || deleted.ReservedSizeBytes != 0 {
				t.Fatalf("cancellation left an import row or reservation active: %+v error=%v", deleted, err)
			}
			if _, err := os.Stat(filepath.Dir(deleted.StoragePath)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancellation left a track/source file behind: %v", err)
			}
			store.clippingMu.Lock()
			_, claimRemains := store.clippingImportWorkspaces[source.ID]
			store.clippingMu.Unlock()
			if claimRemains {
				t.Fatal("cancellation left a YouTube scratch reservation behind")
			}
		})
	}
}

func TestYouTubeSeparateTrackRetryDiscardsFirstTrackAndReextracts(t *testing.T) {
	tracks := c25MakeSyntheticTracks(t)
	for _, failurePath := range []string{"/video-track", "/original-audio"} {
		t.Run(strings.TrimPrefix(failurePath, "/"), func(t *testing.T) {
			_, store, _, acquisition := newClippingAcquisitionFixture(t)
			metadata := c25SeparateMetadata(tracks, true)
			runLog := filepath.Join(t.TempDir(), "extractor-runs")
			c25InstallExtractor(t, acquisition, metadata, runLog, "")
			acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
			var mu sync.Mutex
			failedAttempts := 0
			var requested []string
			acquisition.transportFactory = func(*url.URL, []net.IP) http.RoundTripper {
				return c25RoundTripFunc(func(request *http.Request) (*http.Response, error) {
					mu.Lock()
					requested = append(requested, request.URL.Path)
					failNow := request.URL.Path == failurePath && failedAttempts < clippingYouTubeTrackTransferAttempts
					if failNow {
						failedAttempts++
					}
					mu.Unlock()
					if failNow {
						return nil, errors.New("synthetic transient track connection loss")
					}
					media, contentType := tracks.video, "video/mp4"
					if request.URL.Path == "/original-audio" {
						media, contentType = tracks.audio, "audio/mp4"
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(bytes.NewReader(media)), ContentLength: int64(len(media))}, nil
				})
			}
			c25InstallCountingFFmpeg(t, acquisition, filepath.Join(t.TempDir(), "mux-arguments"))
			acquisition.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
			source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if worked, err := acquisition.ProcessNextImport(context.Background()); err != nil || !worked {
				t.Fatalf("first retryable import worked=%v error=%v", worked, err)
			}
			retrying, err := store.ClippingSource(source.ID)
			if err != nil || retrying.Status != ClippingSourceImporting || retrying.ImportAttempts != 1 {
				t.Fatalf("retryable split import did not remain queued: %+v error=%v", retrying, err)
			}
			paths, err := acquisition.sourcePaths(source.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{paths.temp, paths.videoTrack, paths.audioTrack} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("retry retained partial file %s: %v", filepath.Base(path), err)
				}
			}
			if _, err := store.db.Exec(`UPDATE clipping_sources SET next_import_at=0,import_lease_until=0 WHERE id=?`, source.ID); err != nil {
				t.Fatal(err)
			}
			if worked, err := acquisition.ProcessNextImport(context.Background()); err != nil || !worked {
				t.Fatalf("retried import worked=%v error=%v", worked, err)
			}
			ready, err := store.ClippingSource(source.ID)
			if err != nil || ready.Status != ClippingSourceReady || ready.ImportAttempts != 2 {
				t.Fatalf("metadata re-extraction did not lead to Ready on retry: %+v error=%v", ready, err)
			}
			if runs, err := os.ReadFile(runLog); err != nil || string(runs) != "xx" {
				t.Fatalf("retry did not restart metadata extraction: runs=%q error=%v", runs, err)
			}
			if strings.Contains(ready.ImportExpectation, "signature=") || strings.Contains(ready.ImportExpectation, "googlevideo.com") {
				t.Fatalf("durable expectation gained ephemeral extractor data on retry: %q", ready.ImportExpectation)
			}
		})
	}
}

type c26SignalEOFBody struct {
	reader *bytes.Reader
	done   chan struct{}
	once   sync.Once
}

func (body *c26SignalEOFBody) Read(buffer []byte) (int, error) {
	n, err := body.reader.Read(buffer)
	if errors.Is(err, io.EOF) {
		body.once.Do(func() { close(body.done) })
	}
	return n, err
}

func (body *c26SignalEOFBody) Close() error { return nil }

type c26PartialFailureBody struct {
	peerDone <-chan struct{}
	read     bool
}

func (body *c26PartialFailureBody) Read(buffer []byte) (int, error) {
	if body.read {
		return 0, io.ErrUnexpectedEOF
	}
	<-body.peerDone
	body.read = true
	return copy(buffer, []byte("partial audio")), io.ErrUnexpectedEOF
}

func (body *c26PartialFailureBody) Close() error { return nil }

func TestYouTubeSeparateTracksStartTogetherAndRetryPartialAudioWithoutDiscardingVideo(t *testing.T) {
	tracks := c25MakeSyntheticTracks(t)
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	c25InstallExtractor(t, acquisition, c25SeparateMetadata(tracks, true), "", "")
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}

	var mu sync.Mutex
	startedPaths := map[string]bool{}
	requestCounts := map[string]int{}
	initialBothStarted := make(chan struct{})
	var bothStartedOnce sync.Once
	type retryObservation struct {
		remaining int64
		video     []byte
	}
	retryObserved := make(chan retryObservation, 1)
	videoComplete := make(chan struct{})
	var transportErr string
	var sourceID string
	acquisition.transportFactory = func(*url.URL, []net.IP) http.RoundTripper {
		return c25RoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" || request.Header.Get("Proxy-Authorization") != "" {
				return nil, errors.New("synthetic YouTube request forwarded credentials")
			}
			mu.Lock()
			requestCounts[request.URL.Path]++
			count := requestCounts[request.URL.Path]
			if count == 1 {
				startedPaths[request.URL.Path] = true
				if startedPaths["/video-track"] && startedPaths["/original-audio"] {
					bothStartedOnce.Do(func() { close(initialBothStarted) })
				}
			}
			mu.Unlock()

			if count == 1 {
				select {
				case <-initialBothStarted:
				case <-time.After(5 * time.Second):
					mu.Lock()
					transportErr = "separate tracks did not both start before either response"
					mu.Unlock()
					return nil, errors.New("synthetic concurrent-start barrier timed out")
				}
			}
			if request.URL.Path == "/video-track" {
				return &http.Response{
					StatusCode:    http.StatusOK,
					Header:        http.Header{"Content-Type": []string{"video/mp4"}},
					Body:          &c26SignalEOFBody{reader: bytes.NewReader(tracks.video), done: videoComplete},
					ContentLength: int64(len(tracks.video)),
				}, nil
			}
			if request.URL.Path != "/original-audio" {
				return nil, fmt.Errorf("unexpected synthetic track path %q", request.URL.Path)
			}
			if count == 1 {
				return &http.Response{
					StatusCode:    http.StatusOK,
					Header:        http.Header{"Content-Type": []string{"audio/mp4"}},
					Body:          &c26PartialFailureBody{peerDone: videoComplete},
					ContentLength: int64(len(tracks.audio)),
				}, nil
			}

			paths, err := acquisition.sourcePaths(sourceID)
			if err != nil {
				return nil, err
			}
			video, err := os.ReadFile(paths.videoTrack)
			if err != nil {
				return nil, err
			}
			store.clippingMu.Lock()
			remaining := store.clippingImportWorkspaces[sourceID]
			store.clippingMu.Unlock()
			retryObserved <- retryObservation{remaining: remaining, video: video}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"audio/mp4"}},
				Body:          io.NopCloser(bytes.NewReader(tracks.audio)),
				ContentLength: int64(len(tracks.audio)),
			}, nil
		})
	}

	logPath := filepath.Join(t.TempDir(), "mux-arguments")
	c25InstallCountingFFmpeg(t, acquisition, logPath)
	// The request context is deliberately not used as an identity channel; the
	// fixture records the generated source ID before the importer starts.
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
	if err != nil {
		t.Fatal(err)
	}
	sourceID = source.ID
	if worked, err := acquisition.ProcessNextImport(context.Background()); err != nil || !worked {
		t.Fatalf("concurrent partial-body import worked=%v error=%v", worked, err)
	}
	mu.Lock()
	gotTransportErr := transportErr
	gotRequestCounts := map[string]int{
		"/video-track":    requestCounts["/video-track"],
		"/original-audio": requestCounts["/original-audio"],
	}
	mu.Unlock()
	if gotTransportErr != "" {
		t.Fatal(gotTransportErr)
	}
	if gotRequestCounts["/video-track"] != 1 || gotRequestCounts["/original-audio"] != 2 {
		t.Fatalf("unexpected bounded transfer attempts: %v", gotRequestCounts)
	}
	select {
	case observation := <-retryObserved:
		if observation.remaining != clippingYouTubeTrackWorkspaceBytes-int64(len(tracks.video)) {
			t.Fatalf("partial audio bytes were not refunded while the completed video charge remained: remaining=%d video=%d want=%d", observation.remaining, len(tracks.video), clippingYouTubeTrackWorkspaceBytes-int64(len(tracks.video)))
		}
		if !bytes.Equal(observation.video, tracks.video) {
			t.Fatal("retry reset discarded or changed the successfully downloaded video track")
		}
	default:
		t.Fatal("audio retry did not expose its reservation/file state before writing the second response")
	}
	ready, err := store.ClippingSource(source.ID)
	if err != nil || ready.Status != ClippingSourceReady {
		t.Fatalf("transient audio body failure did not recover to Ready: status=%s error=%v", ready.Status, err)
	}
	if !c26HasDiagnostic(ready.Diagnostics, ClippingDiagnosticStageAudioDownload, ClippingDiagnosticRetrying, "track_body_incomplete") ||
		!c26HasDiagnostic(ready.Diagnostics, ClippingDiagnosticStageAudioDownload, ClippingDiagnosticCompleted, "") ||
		!c26HasDiagnostic(ready.Diagnostics, ClippingDiagnosticStageVideoDownload, ClippingDiagnosticCompleted, "") {
		t.Fatalf("track diagnostics did not preserve attempt retry and successful peer completion: %+v", ready.Diagnostics)
	}
}

func TestYouTubeSeparateTrackFailureCancelsPeerAndBoundsRetryAttempts(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	c25InstallExtractor(t, acquisition, c25TinySeparateMetadata(
		"https://rr1---sn.googlevideo.com/video-track?signature=v",
		"https://rr1---sn.googlevideo.com/original-audio?signature=a", 0, 0), "", "")
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
	var mu sync.Mutex
	startedPaths := map[string]bool{}
	requestCounts := map[string]int{}
	initialBothStarted := make(chan struct{})
	var bothStartedOnce sync.Once
	peerBodyStarted := make(chan struct{})
	acquisition.transportFactory = func(*url.URL, []net.IP) http.RoundTripper {
		return c25RoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" || request.Header.Get("Proxy-Authorization") != "" {
				return nil, errors.New("synthetic YouTube request forwarded credentials")
			}
			mu.Lock()
			requestCounts[request.URL.Path]++
			count := requestCounts[request.URL.Path]
			if count == 1 {
				startedPaths[request.URL.Path] = true
				if startedPaths["/video-track"] && startedPaths["/original-audio"] {
					bothStartedOnce.Do(func() { close(initialBothStarted) })
				}
			}
			mu.Unlock()
			if count == 1 {
				select {
				case <-initialBothStarted:
				case <-time.After(5 * time.Second):
					return nil, errors.New("synthetic concurrent-start barrier timed out")
				}
			}
			if request.URL.Path == "/video-track" {
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("temporary")), ContentLength: -1}, nil
			}
			if request.URL.Path == "/original-audio" {
				contentType := "audio/mp4"
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: &c25BlockingBody{ctx: request.Context(), started: peerBodyStarted}, ContentLength: -1}, nil
			}
			return nil, fmt.Errorf("unexpected synthetic track path %q", request.URL.Path)
		})
	}
	acquisition.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := acquisition.ProcessNextImport(context.Background()); err != nil || !worked {
		t.Fatalf("bounded failed track import worked=%v error=%v", worked, err)
	}
	select {
	case <-peerBodyStarted:
	default:
		t.Fatal("peer response body never started before primary failure cancellation")
	}
	mu.Lock()
	gotRequestCounts := map[string]int{
		"/video-track":    requestCounts["/video-track"],
		"/original-audio": requestCounts["/original-audio"],
	}
	mu.Unlock()
	if gotRequestCounts["/video-track"] != clippingYouTubeTrackTransferAttempts || gotRequestCounts["/original-audio"] != 1 {
		t.Fatalf("primary retry bound or peer cancellation was violated: %v", gotRequestCounts)
	}
	retrying, err := store.ClippingSource(source.ID)
	if err != nil || retrying.Status != ClippingSourceImporting || retrying.ImportAttempts != 1 {
		t.Fatalf("primary transient failure did not schedule the durable metadata retry: status=%s attempts=%d error=%v", retrying.Status, retrying.ImportAttempts, err)
	}
	if !c26HasDiagnostic(retrying.Diagnostics, ClippingDiagnosticStageVideoDownload, ClippingDiagnosticFailed, "track_http_retryable") ||
		!c26HasDiagnostic(retrying.Diagnostics, ClippingDiagnosticStageAudioDownload, ClippingDiagnosticFailed, "track_transfer_aborted") ||
		!c26HasDiagnostic(retrying.Diagnostics, ClippingDiagnosticStageDispatch, ClippingDiagnosticRetrying, "retry_scheduled") {
		t.Fatalf("diagnostics did not distinguish the primary retryable failure from its canceled peer: %+v", retrying.Diagnostics)
	}
	paths, err := acquisition.sourcePaths(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.videoTrack, paths.audioTrack} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("durable retry retained a partial track %s: %v", filepath.Base(path), err)
		}
	}
}

func TestYouTubeDeniedTrackStatusesTriggerFreshMetadataRetry(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusGone} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			_, store, _, acquisition := newClippingAcquisitionFixture(t)
			c25InstallExtractor(t, acquisition, c25TinySeparateMetadata(
				"https://rr1---sn.googlevideo.com/video-track?signature=v",
				"https://rr1---sn.googlevideo.com/original-audio?signature=a", 0, 0), "", "")
			acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
			var mu sync.Mutex
			requestCounts := map[string]int{}
			acquisition.transportFactory = func(*url.URL, []net.IP) http.RoundTripper {
				return c25RoundTripFunc(func(request *http.Request) (*http.Response, error) {
					if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" || request.Header.Get("Proxy-Authorization") != "" {
						return nil, errors.New("synthetic YouTube request forwarded credentials")
					}
					mu.Lock()
					requestCounts[request.URL.Path]++
					mu.Unlock()
					if request.URL.Path == "/video-track" {
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"video/mp4"}}, Body: io.NopCloser(strings.NewReader("video")), ContentLength: 5}, nil
					}
					if request.URL.Path == "/original-audio" {
						return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("denied")), ContentLength: 6}, nil
					}
					return nil, fmt.Errorf("unexpected synthetic track path %q", request.URL.Path)
				})
			}
			source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if worked, err := acquisition.ProcessNextImport(context.Background()); err != nil || !worked {
				t.Fatalf("denied track import worked=%v error=%v", worked, err)
			}
			mu.Lock()
			gotRequestCounts := map[string]int{
				"/video-track":    requestCounts["/video-track"],
				"/original-audio": requestCounts["/original-audio"],
			}
			mu.Unlock()
			if gotRequestCounts["/video-track"] > 1 || gotRequestCounts["/original-audio"] != 1 {
				t.Fatalf("access-denied status was retried against the same signed URL: %v", gotRequestCounts)
			}
			retrying, err := store.ClippingSource(source.ID)
			if err != nil || retrying.Status != ClippingSourceImporting || retrying.ImportAttempts != 1 {
				t.Fatalf("access-denied track did not schedule a fresh metadata attempt: status=%s attempts=%d error=%v", retrying.Status, retrying.ImportAttempts, err)
			}
			if !c26HasDiagnostic(retrying.Diagnostics, ClippingDiagnosticStageAudioDownload, ClippingDiagnosticFailed, "track_access_denied") ||
				!c26HasDiagnostic(retrying.Diagnostics, ClippingDiagnosticStageDispatch, ClippingDiagnosticRetrying, "retry_scheduled") {
				t.Fatalf("access-denied status was mislabeled or not retried safely: %+v", retrying.Diagnostics)
			}
			paths, err := acquisition.sourcePaths(source.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{paths.videoTrack, paths.audioTrack} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("fresh metadata retry retained partial track %s: %v", filepath.Base(path), err)
				}
			}
		})
	}
}

func TestYouTubePeerFailureDuringTrackRetryWaitIsRecorded(t *testing.T) {
	previousDelay := clippingYouTubeTrackRetryDelay
	clippingYouTubeTrackRetryDelay = 2 * time.Second
	defer func() { clippingYouTubeTrackRetryDelay = previousDelay }()

	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	c25InstallExtractor(t, acquisition, c25TinySeparateMetadata(
		"https://rr1---sn.googlevideo.com/video-track?signature=v",
		"https://rr1---sn.googlevideo.com/original-audio?signature=a", 0, 0), "", "")
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}
	var sourceID string
	var mu sync.Mutex
	requestCounts := map[string]int{}
	acquisition.transportFactory = func(*url.URL, []net.IP) http.RoundTripper {
		return c25RoundTripFunc(func(request *http.Request) (*http.Response, error) {
			mu.Lock()
			requestCounts[request.URL.Path]++
			mu.Unlock()
			switch request.URL.Path {
			case "/video-track":
				return nil, errors.New("synthetic transient video connection loss")
			case "/original-audio":
				if !c26WaitForDiagnostic(store, sourceID, ClippingDiagnosticStageVideoDownload, ClippingDiagnosticRetrying, "track_request_failed", 5*time.Second) {
					return nil, errors.New("video retry diagnostic was not written before peer failure")
				}
				return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("missing")), ContentLength: 7}, nil
			default:
				return nil, fmt.Errorf("unexpected synthetic track path %q", request.URL.Path)
			}
		})
	}
	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
	if err != nil {
		t.Fatal(err)
	}
	sourceID = source.ID
	if worked, err := acquisition.ProcessNextImport(context.Background()); err != nil || !worked {
		t.Fatalf("peer failure during local retry wait worked=%v error=%v", worked, err)
	}
	mu.Lock()
	gotRequestCounts := map[string]int{
		"/video-track":    requestCounts["/video-track"],
		"/original-audio": requestCounts["/original-audio"],
	}
	mu.Unlock()
	if gotRequestCounts["/video-track"] != 1 || gotRequestCounts["/original-audio"] != 1 {
		t.Fatalf("peer failure did not interrupt the video retry delay: %v", gotRequestCounts)
	}
	failed, err := store.ClippingSource(source.ID)
	if err != nil || failed.Status != ClippingSourceFailed {
		t.Fatalf("permanent peer status did not fail and clean the source: status=%s error=%v", failed.Status, err)
	}
	if !c26HasDiagnostic(failed.Diagnostics, ClippingDiagnosticStageVideoDownload, ClippingDiagnosticRetrying, "track_request_failed") ||
		!c26HasDiagnostic(failed.Diagnostics, ClippingDiagnosticStageVideoDownload, ClippingDiagnosticFailed, "track_transfer_aborted") ||
		!c26HasDiagnostic(failed.Diagnostics, ClippingDiagnosticStageAudioDownload, ClippingDiagnosticFailed, "track_content_invalid") {
		t.Fatalf("peer cancellation during a retry delay lost its terminal stage or primary error: %+v", failed.Diagnostics)
	}
}

func c26WaitForDiagnostic(store *Store, sourceID, stage string, status ClippingSourceDiagnosticStatus, code string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		source, err := store.ClippingSource(sourceID)
		if err == nil && c26HasDiagnostic(source.Diagnostics, stage, status, code) {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func c26HasDiagnostic(diagnostics []ClippingSourceDiagnostic, stage string, status ClippingSourceDiagnosticStatus, code string) bool {
	for _, item := range diagnostics {
		if item.Stage == stage && item.Status == status && item.Code == code {
			return true
		}
	}
	return false
}

func TestYouTubeSeparateTrackRejectedRedirectKeepsFormatDiagnostic(t *testing.T) {
	_, store, _, acquisition := newClippingAcquisitionFixture(t)
	acquisition.resolver = clippingTestResolver{{IP: net.ParseIP("8.8.8.8")}}

	var mu sync.Mutex
	requestCounts := map[string]int{}
	acquisition.transportFactory = func(_ *url.URL, addresses []net.IP) http.RoundTripper {
		if len(addresses) != 1 || !addresses[0].Equal(net.ParseIP("8.8.8.8")) {
			return c25RoundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("synthetic media transport did not receive the pinned public address")
			})
		}
		return c25RoundTripFunc(func(request *http.Request) (*http.Response, error) {
			mu.Lock()
			requestCounts[request.URL.Path]++
			mu.Unlock()
			switch request.URL.Path {
			case "/video-track":
				return &http.Response{
					StatusCode: http.StatusFound,
					Header:     http.Header{"Location": []string{"https://evil.test/video?signature=redirect-secret"}},
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			case "/original-audio":
				return &http.Response{
					StatusCode:    http.StatusOK,
					Header:        http.Header{"Content-Type": []string{"audio/mp4"}},
					Body:          io.NopCloser(strings.NewReader("audio")),
					ContentLength: 5,
				}, nil
			default:
				return nil, fmt.Errorf("unexpected synthetic track path %q", request.URL.Path)
			}
		})
	}

	source, err := acquisition.CreatePublicImport(context.Background(), true, "https://youtu.be/"+c25SyntheticVideoID, 0)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := acquisition.sourcePaths(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	mediaURL, err := url.Parse("https://rr1---sn.googlevideo.com/video-track?signature=video-secret")
	if err != nil {
		t.Fatal(err)
	}
	trace := newClippingDiagnosticTrace(acquisition, source.ID)
	trackErr := acquisition.downloadYouTubeTrack(context.Background(), source.ID,
		youtubeSelection{URL: mediaURL}, youtubeFormat{Extension: "mp4"}, paths.videoTrack, "video", MaxClippingSourceBytes, trace)
	if trackErr == nil || isRetryableYouTubeTrackTransfer(trackErr) {
		t.Fatalf("permanent redirect rejection error=%v; want a terminal track failure", trackErr)
	}

	observed, err := store.ClippingSource(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !c26HasDiagnostic(observed.Diagnostics, ClippingDiagnosticStageVideoDownload, ClippingDiagnosticFailed, "format_unavailable") ||
		c26HasDiagnostic(observed.Diagnostics, ClippingDiagnosticStageVideoDownload, ClippingDiagnosticFailed, "track_content_invalid") {
		t.Fatalf("rejected redirect lost its existing format diagnostic: %+v", observed.Diagnostics)
	}
	mu.Lock()
	videoRequests := requestCounts["/video-track"]
	mu.Unlock()
	if videoRequests != 1 {
		t.Fatalf("permanent redirect rejection made %d requests to the selected video URL; want one", videoRequests)
	}
	if strings.Contains(trackErr.Error(), "signature=") || strings.Contains(trackErr.Error(), "evil.test") {
		t.Fatalf("redirect details leaked into the safe track failure: %q", trackErr)
	}
}
