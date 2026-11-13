# Gemini provider foundation

G1 introduced an isolated provider foundation. G3 connects it to the opt-in Go executor; public dashboard/API submission follows in G4. It sends one canonical public YouTube watch URL directly as a video part. It never acquires the source file. The official Go SDK is pinned to `google.golang.org/genai v1.77.0`; the module requires Go 1.24 or newer.

## Request and authentication

Use the Google Cloud backend (`BackendEnterprise`, normalized by the SDK to Vertex AI), model `gemini-3.8-flash`, API `v1`, and explicit `global`, `us` or `eu` routing. The application derives a fixed Google endpoint; caller-provided endpoints and API keys are not configuration options. Production authentication uses Application Default Credentials with the cloud-platform scope. With a custom HTTP client, the SDK's `UseDefaultCredentials()` method must install authorization middleware explicitly.

The video part uses `video/mp4` and `STATIC` processing. The model receives a versioned editorial prompt and JSON response schema. It owns hooks, candidate selection, proposed boundaries, titles, explanations, scores and ordering. The backend accepts valid output without reordering, rescoring or repairing it. Fewer candidates, including zero, are valid when accompanied by an explanation.

SDK retries are explicitly one attempt. Automatic continuation is disabled and the unsupported `candidate_count` setting is omitted. Editorial candidate limits are separate from the number of alternative model responses. Requests have a five-minute deadline, redirects are refused, and response bodies are bounded to 1 MiB before SDK decoding. The result contract bounds both raw JSON and its stored serialization to 256 KiB.

## Limits and uncertainty

Google's YouTube input support is Preview. A request contains one public video; account access, private content and authentication bypass are unsupported. The initial accepted model-reported duration is at most 40 minutes. This conservative application limit does not prove that Google inspected the complete video, and rejection after inference does not avoid the charge. The contract's wider defensive duration bound is not a claim of four-hour request support.

A valid result is a proposed editorial analysis. Source duration is explicitly model-reported, scores are editorial estimates, and clip timestamps require independent media verification during future extraction. Token usage and model-response identifiers are retained when available; missing usage is unknown and no invoice amount is invented.

Errors distinguish work known not to have been submitted, explicit rejection, a received but unusable result, and an unknown outcome after submission. An ambiguous timeout, transport failure, or malformed outer response must not cause automatic inference replay. G3 implements orchestration and durable outcome handling. No automatic correction request is implemented in G1.

The deterministic request fingerprint includes the canonical URL, routing, model, options, static settings and exact prompt/schema digests. It identifies a request, not downloaded media. Changes to video content at the same URL cannot be detected from that digest alone; G2 implements bounded cache lifetimes and source-scoped identity.

## Primary documentation

Integration decisions were checked against current Google documentation and the pinned SDK during G1. Documentation evidence and offline wire tests are distinct from a live endpoint test.

- [Gemini 3.8 Flash guide](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/guides/gemini-3-8-flash): supported generation settings and model limitations.
- [Inference reference](https://docs.cloud.google.com/gemini-enterprise-agent-platform/reference/models/inference): video parts and static media processing.
- [Video understanding](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/multimodal/video-understanding): public YouTube URL input and Preview restrictions.
- [Application Default Credentials](https://docs.cloud.google.com/docs/authentication/application-default-credentials): supported identity sources.
- [Pinned Go SDK source](https://github.com/googleapis/go-genai/tree/v1.77.0): backend normalization, custom-client authorization, retry defaults and continuation behavior.

Live ADC authentication, YouTube availability, semantic quality, supported language performance, latency and cost remain unverified. No live inference or deployment is part of G1's offline acceptance. See the [migration plan](GEMINI-MIGRATION.md) for the later release gates.
