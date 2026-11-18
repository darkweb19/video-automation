package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestPrepareClippingGeminiRequestUsesDirectYouTubeVideoAndSchema(t *testing.T) {
	prepared, err := prepareClippingGeminiRequest(testClippingGeminiConfig(), testClippingGeminiRequest("https://youtu.be/abcdefghijk?feature=share"))
	if err != nil {
		t.Fatalf("prepareClippingGeminiRequest() error = %v", err)
	}
	if prepared.Model != clippingGeminiModel {
		t.Errorf("Model = %q, want %q", prepared.Model, clippingGeminiModel)
	}
	if prepared.CanonicalYouTubeURL != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Errorf("CanonicalYouTubeURL = %q", prepared.CanonicalYouTubeURL)
	}
	if prepared.Content == nil || prepared.Content.Role != genai.RoleUser || len(prepared.Content.Parts) != 2 {
		t.Fatalf("content = %#v, want one user message with video and prompt parts", prepared.Content)
	}
	video := prepared.Content.Parts[0]
	if video.FileData == nil || video.FileData.FileURI != prepared.CanonicalYouTubeURL || video.FileData.MIMEType != "video/mp4" || video.InlineData != nil {
		t.Errorf("first part = %#v, want direct YouTube FileData with video/mp4", video)
	}
	if video.MediaProcessing != genai.MediaProcessingStatic {
		t.Errorf("MediaProcessing = %q, want explicit STATIC", video.MediaProcessing)
	}
	contentJSON, err := json.Marshal(prepared.Content)
	if err != nil {
		t.Fatalf("marshal request content: %v", err)
	}
	if !strings.Contains(string(contentJSON), `"mediaProcessing":"STATIC"`) {
		t.Errorf("wire content does not contain explicit STATIC media processing: %s", contentJSON)
	}
	if prepared.Content.Parts[1].Text != prepared.PromptText || prepared.Content.Parts[1].FileData != nil {
		t.Errorf("second part = %#v, want prompt text only", prepared.Content.Parts[1])
	}
	if prepared.GenerationConfig == nil {
		t.Fatal("GenerationConfig is nil")
	}
	if prepared.GenerationConfig.AutomaticContinuation == nil || *prepared.GenerationConfig.AutomaticContinuation {
		t.Errorf("AutomaticContinuation = %v, want explicitly disabled", prepared.GenerationConfig.AutomaticContinuation)
	}
	if prepared.GenerationConfig.CandidateCount != 0 {
		t.Errorf("CandidateCount = %d, want omitted because Gemini 3.8 rejects candidate_count", prepared.GenerationConfig.CandidateCount)
	}
	configJSON, err := json.Marshal(prepared.GenerationConfig)
	if err != nil {
		t.Fatalf("marshal GenerationConfig: %v", err)
	}
	if strings.Contains(string(configJSON), "candidateCount") {
		t.Errorf("wire config contains unsupported candidateCount: %s", configJSON)
	}
	if prepared.GenerationConfig.MaxOutputTokens != clippingGeminiMaxOutputTokens {
		t.Errorf("MaxOutputTokens = %d, want %d", prepared.GenerationConfig.MaxOutputTokens, clippingGeminiMaxOutputTokens)
	}
	if prepared.GenerationConfig.ResponseMIMEType != clippingGeminiResponseMIMEType || prepared.GenerationConfig.ResponseSchema != prepared.Schema {
		t.Errorf("structured output config mismatch: mime=%q schemaMatches=%t", prepared.GenerationConfig.ResponseMIMEType, prepared.GenerationConfig.ResponseSchema == prepared.Schema)
	}
	if prepared.PromptVersion != clippingGeminiPromptVersion || prepared.SchemaVersion != ClippingGeminiAnalysisVersion {
		t.Errorf("prompt/schema versions = %q/%q", prepared.PromptVersion, prepared.SchemaVersion)
	}
	if got := sha256Hex([]byte(prepared.PromptText)); got != prepared.PromptSHA256 {
		t.Errorf("prompt SHA-256 = %q, want %q", prepared.PromptSHA256, got)
	}
	schemaJSON, err := json.Marshal(prepared.Schema)
	if err != nil {
		t.Fatalf("marshal response schema: %v", err)
	}
	if got := sha256Hex(schemaJSON); got != prepared.SchemaSHA256 {
		t.Errorf("schema SHA-256 = %q, want %q", prepared.SchemaSHA256, got)
	}
	if len(prepared.Fingerprint) != 64 {
		t.Fatalf("fingerprint length = %d, want 64 lowercase hex chars", len(prepared.Fingerprint))
	}
	if _, err := hex.DecodeString(prepared.Fingerprint); err != nil {
		t.Errorf("fingerprint is not hex: %v", err)
	}
}

func TestClippingGeminiRequestFingerprintCanonicalizesPublicYouTubeURLs(t *testing.T) {
	urls := []string{
		"https://youtu.be/abcdefghijk?feature=share",
		"https://www.youtube.com/watch?v=abcdefghijk",
		"https://youtube.com/shorts/abcdefghijk",
		"https://m.youtube.com/embed/abcdefghijk?start=2",
	}
	var first string
	for i, raw := range urls {
		fingerprint, err := clippingGeminiRequestFingerprint(testClippingGeminiConfig(), testClippingGeminiRequest(raw))
		if err != nil {
			t.Fatalf("clippingGeminiRequestFingerprint(%q) error = %v", raw, err)
		}
		if i == 0 {
			first = fingerprint
		} else if fingerprint != first {
			t.Errorf("canonical-equivalent URL %q got fingerprint %q, want %q", raw, fingerprint, first)
		}
	}
}

func TestClippingGeminiRequestFingerprintIncludesImmutableRequestSettings(t *testing.T) {
	baseConfig := testClippingGeminiConfig()
	baseRequest := testClippingGeminiRequest("https://www.youtube.com/watch?v=abcdefghijk")
	base, err := clippingGeminiRequestFingerprint(baseConfig, baseRequest)
	if err != nil {
		t.Fatalf("base fingerprint error = %v", err)
	}
	changedProject := baseConfig
	changedProject.ProjectID = "framevault-other-123"
	changedProjectFingerprint, err := clippingGeminiRequestFingerprint(changedProject, baseRequest)
	if err != nil {
		t.Fatalf("changed project fingerprint error = %v", err)
	}
	if changedProjectFingerprint == base {
		t.Error("changing project ID did not change fingerprint")
	}
	changedLocation := baseConfig
	changedLocation.Location = "us"
	changedLocationFingerprint, err := clippingGeminiRequestFingerprint(changedLocation, baseRequest)
	if err != nil {
		t.Fatalf("changed location fingerprint error = %v", err)
	}
	if changedLocationFingerprint == base {
		t.Error("changing location did not change fingerprint")
	}
	changedOptions := baseRequest
	changedOptions.Options.CandidateLimit--
	changedOptionsFingerprint, err := clippingGeminiRequestFingerprint(baseConfig, changedOptions)
	if err != nil {
		t.Fatalf("changed options fingerprint error = %v", err)
	}
	if changedOptionsFingerprint == base {
		t.Error("changing options did not change fingerprint")
	}
}

func TestPrepareClippingGeminiRequestRejectsUnsafeOrInvalidInput(t *testing.T) {
	unsafeURL := "https://attacker.example/path?credential=do-not-echo"
	_, err := prepareClippingGeminiRequest(testClippingGeminiConfig(), testClippingGeminiRequest(unsafeURL))
	if !errors.Is(err, ErrClippingGeminiInvalidRequest) {
		t.Fatalf("invalid URL error = %v, want ErrClippingGeminiInvalidRequest", err)
	}
	if strings.Contains(err.Error(), unsafeURL) || strings.Contains(err.Error(), "do-not-echo") {
		t.Errorf("invalid URL error contains caller input: %v", err)
	}
	badOptions := testClippingGeminiRequest("https://www.youtube.com/watch?v=abcdefghijk")
	badOptions.Options.CandidateLimit = 11
	if _, err := prepareClippingGeminiRequest(testClippingGeminiConfig(), badOptions); !errors.Is(err, ErrClippingGeminiInvalidOptions) {
		t.Errorf("invalid options error = %v, want ErrClippingGeminiInvalidOptions", err)
	}
	badConfig := testClippingGeminiConfig()
	badConfig.Location = "https://attacker.example"
	if _, err := prepareClippingGeminiRequest(badConfig, testClippingGeminiRequest("https://www.youtube.com/watch?v=abcdefghijk")); !errors.Is(err, ErrClippingGeminiInvalidConfig) {
		t.Errorf("invalid config error = %v, want ErrClippingGeminiInvalidConfig", err)
	}
}

func testClippingGeminiConfig() ClippingGeminiConfig {
	return ClippingGeminiConfig{ProjectID: "framevault-test-123", Location: "global"}
}

func testClippingGeminiRequest(rawURL string) ClippingGeminiRequest {
	return ClippingGeminiRequest{
		YouTubeURL: rawURL,
		Options: ClippingGeminiOptions{
			ContentType:    "general",
			MinClipMS:      15_000,
			MaxClipMS:      180_000,
			CandidateLimit: 5,
		},
	}
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
