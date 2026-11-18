package app

import "testing"

func TestClippingGeminiPublicArtifactsPreservePayloadAndRejectTampering(t *testing.T) {
	options := testClippingGeminiRequest("").Options
	options.CandidateLimit = 2
	payload := readClippingGeminiFixture(t, "ranked.json")
	artifact, err := clippingGeminiArtifactFromPayload("clipjob_fixture", payload, options)
	if err != nil {
		t.Fatal(err)
	}
	views, err := clippingGeminiPublicArtifacts("clipjob_fixture", options, []ClippingArtifact{artifact})
	if err != nil || len(views) != 1 || views[0].PayloadJSON != string(payload) || views[0].SchemaVersion != ClippingGeminiAnalysisVersion {
		t.Fatalf("public artifact changed: %+v, %v", views, err)
	}
	for _, tc := range []struct {
		name     string
		artifact ClippingArtifact
		options  ClippingGeminiOptions
	}{
		{"wrong job", func() ClippingArtifact { a := artifact; a.JobID = "clipjob_other"; return a }(), options},
		{"legacy schema", func() ClippingArtifact { a := artifact; a.SchemaVersion = "framevault.clipping.analysis.v2"; return a }(), options},
		{"invalid payload", func() ClippingArtifact {
			a := artifact
			a.PayloadJSON = `{"version":"framevault.clipping.analysis.v3","raw_url":"https://example.com"}`
			return a
		}(), options},
		{"invalid ranges", func() ClippingArtifact {
			a := artifact
			a.TimeRanges = append([]ClippingTimeRange(nil), a.TimeRanges...)
			a.TimeRanges[0].StartMS++
			return a
		}(), options},
		{"wrong options", artifact, ClippingGeminiOptions{ContentType: "general", MinClipMS: 15000, MaxClipMS: 180000, CandidateLimit: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := clippingGeminiPublicArtifacts("clipjob_fixture", tc.options, []ClippingArtifact{tc.artifact}); err == nil {
				t.Fatal("accepted inconsistent or invalid artifact")
			}
		})
	}
	if views, err := clippingGeminiPublicArtifacts("clipjob_fixture", options, nil); err != nil || len(views) != 0 {
		t.Fatalf("queued job artifacts: %v, %v", views, err)
	}
}
