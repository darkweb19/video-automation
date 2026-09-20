# OpenRouter AI Video Generator

Small Go MVP that serves a vanilla browser UI and exposes a unified API for OpenRouter video generation. It uses only `OPENROUTER_API_KEY`; the application does not load `.env` files automatically.

## Run

Bash:

```bash
export OPENROUTER_API_KEY="your_openrouter_api_key"
go run .
```

PowerShell:

```powershell
$env:OPENROUTER_API_KEY = "your_openrouter_api_key"
go run .
```

Then open <http://localhost:8080>. The key is required at startup. Video generation is a paid/provider-dependent operation; this repository has not made a real generation request.

## API

Health check:

```bash
curl http://localhost:8080/health
```

List currently available OpenRouter video models:

```bash
curl http://localhost:8080/models
```

Submit a generation (replace the model with an ID returned by `/models`; `google/veo-3.1` is a documented example and availability can change):

```bash
curl -X POST http://localhost:8080/generate \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "A cinematic drone shot over Toronto at night",
    "model": "google/veo-3.1",
    "duration": 8,
    "aspect_ratio": "9:16"
  }'
```

Use the returned ID to poll:

```bash
curl "http://localhost:8080/status?id=GENERATION_ID"
```

When completed, the browser uses `/video?id=GENERATION_ID` to stream the provider output.

Duration and aspect ratio choices come directly from each model's metadata. If OpenRouter does not publish either capability for a model, the UI shows `Provider default`, disables that control, and omits the field from the generation request rather than guessing a supported value. The API and metadata follow OpenRouter's current documentation and may evolve with the provider:

- <https://openrouter.ai/docs/api-reference/overview>
- <https://openrouter.ai/docs/guides/overview/multimodal/video-generation>
- <https://openrouter.ai/models>

## Verification

```bash
gofmt -w *.go
go vet ./...
go test ./...
```

The browser code can be syntax checked with `node --check static/app.js`. Browser visual verification requires a local browser; no real OpenRouter key or paid generation is included in tests.
