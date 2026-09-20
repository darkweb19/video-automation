(() => {
  "use strict";

  let maxPromptLength = 4000;
  const POLL_INTERVAL_MS = 5000;
  const MAX_TRANSIENT_STATUS_ERRORS = 3;
  const form = document.querySelector("#generator-form");
  const prompt = document.querySelector("#prompt");
  const promptCount = document.querySelector("#prompt-count");
  const promptError = document.querySelector("#prompt-error");
  const model = document.querySelector("#model");
  const duration = document.querySelector("#duration");
  const aspectRatio = document.querySelector("#aspect-ratio");
  const generate = document.querySelector("#generate");
  const statusLabel = document.querySelector("#status-label");
  const statusDetail = document.querySelector("#status-detail");
  const progressWrap = document.querySelector("#progress-wrap");
  const progressBar = document.querySelector("#progress-bar");
  const errorBox = document.querySelector("#error");
  const retryStatusButton = document.querySelector("#retry-status");
  const videoSection = document.querySelector("#video-section");
  const video = document.querySelector("#generated-video");
  const openVideo = document.querySelector("#open-video");
  let currentGenerationId = null;
  let pollTimer = null;
  let pollController = null;
  let statusErrors = 0;
  let active = false;
  let models = [];
  let generationToken = 0;

  const json = async (response) => {
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || `Request failed (${response.status})`);
    return body;
  };
  const setError = (message) => { errorBox.textContent = message || ""; errorBox.hidden = !message; };
  const setOptions = (select, values, label) => {
    select.replaceChildren();
    if (!values.length) { const option = new Option("Provider default", ""); select.add(option); select.disabled = true; return; }
    values.forEach((value) => select.add(new Option(String(value), String(value))));
    select.disabled = false;
  };
  const selectedModel = () => models.find((item) => item.id === model.value);
  const updateOptions = () => {
    const chosen = selectedModel();
    const durations = chosen && Array.isArray(chosen.durations) && chosen.durations.length ? chosen.durations : [];
    const ratios = chosen && Array.isArray(chosen.aspect_ratios) && chosen.aspect_ratios.length ? chosen.aspect_ratios : [];
    setOptions(duration, durations, "durations");
    setOptions(aspectRatio, ratios, "aspect ratios");
    if (durations.includes(8)) duration.value = "8";
    if (ratios.includes("9:16")) aspectRatio.value = "9:16";
  };
  const updateGenerateEnabled = () => { generate.disabled = active || !prompt.value.trim() || Array.from(prompt.value).length > maxPromptLength || !model.value || model.disabled; };
  const renderStatus = (state, progress) => {
    statusLabel.textContent = state.charAt(0).toUpperCase() + state.slice(1);
    progressWrap.hidden = !["queued", "processing"].includes(state);
    progressWrap.classList.toggle("indeterminate", typeof progress !== "number");
    if (typeof progress === "number" && Number.isFinite(progress)) {
      const value = Math.max(0, Math.min(100, progress)); progressBar.style.width = `${value}%`; progressBar.setAttribute("aria-valuenow", value); statusDetail.textContent = `${value}%`;
    } else { progressBar.style.removeProperty("width"); progressBar.removeAttribute("aria-valuenow"); statusDetail.textContent = state === "processing" ? "Waiting for provider progress..." : ""; }
  };
  const cleanupPolling = () => { if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; } if (pollController) { pollController.abort(); pollController = null; } };
  const renderVideo = (id, outputURL) => {
    const url = typeof outputURL === "string" && /^https:\/\//i.test(outputURL) ? outputURL : `/video?id=${encodeURIComponent(id)}`;
    video.src = url;
    openVideo.href = url;
    videoSection.hidden = false;
  };
  const schedulePoll = () => { cleanupPolling(); pollTimer = setTimeout(() => pollGeneration(), POLL_INTERVAL_MS); };
  async function pollGeneration(token = generationToken) {
    pollTimer = null;
    const id = currentGenerationId;
    if (!id || !active || token !== generationToken) return;
    pollController = new AbortController();
    try {
      const response = await fetch(`/status?id=${encodeURIComponent(id)}`, { signal: pollController.signal });
      const data = await json(response);
      if (id !== currentGenerationId || token !== generationToken) return;
      statusErrors = 0; retryStatusButton.hidden = true; setError(""); renderStatus(data.status || "processing", data.progress);
      if (data.status === "completed") { active = false; cleanupPolling(); renderVideo(id, data.output_url); generate.textContent = "Generate Video"; updateGenerateEnabled(); return; }
      if (data.status === "failed") { active = false; cleanupPolling(); setError(data.error || "Video generation failed."); generate.textContent = "Generate Video"; updateGenerateEnabled(); return; }
      schedulePoll();
    } catch (error) {
      if (error.name === "AbortError" || id !== currentGenerationId || token !== generationToken) return;
      statusErrors += 1; setError("Unable to update generation status. Retrying...");
      if (statusErrors <= MAX_TRANSIENT_STATUS_ERRORS) schedulePoll(); else { retryStatusButton.hidden = false; statusDetail.textContent = "Status updates stopped after repeated errors."; }
    } finally { pollController = null; }
  }
  async function loadModels() {
    try {
      const data = await json(await fetch("/models"));
      if (Number.isInteger(data.max_prompt_length) && data.max_prompt_length > 0) {
        maxPromptLength = data.max_prompt_length;
        promptCount.textContent = `${Array.from(prompt.value).length} / ${maxPromptLength}`;
      }
      models = Array.isArray(data.models) ? data.models.filter((item) => item && item.id) : [];
      model.replaceChildren(); models.forEach((item) => model.add(new Option(item.name || item.id, item.id)));
      if (!models.length) throw new Error("No video models are currently available.");
      model.disabled = false; updateOptions(); setError(""); updateGenerateEnabled();
    } catch (error) { model.replaceChildren(new Option("Unable to load models", "")); model.disabled = true; setError(`Unable to load models. ${error.message}`); }
  }
  async function submitGeneration(event) {
    event.preventDefault();
    if (active) return;
    const value = prompt.value.trim(); promptError.textContent = ""; setError("");
    if (!value) { promptError.textContent = "Prompt is required."; return; }
    if (Array.from(value).length > maxPromptLength) { promptError.textContent = `Prompt must be ${maxPromptLength} characters or fewer.`; updateGenerateEnabled(); return; }
    cleanupPolling(); currentGenerationId = null; generationToken += 1; const token = generationToken; active = true; statusErrors = 0; retryStatusButton.hidden = true; videoSection.hidden = true; video.removeAttribute("src"); generate.disabled = true; generate.textContent = "Submitting..."; renderStatus("submitting");
    const payload = { prompt: value, model: model.value };
    if (duration.value) payload.duration = Number(duration.value);
    if (aspectRatio.value) payload.aspect_ratio = aspectRatio.value;
    try {
      const data = await json(await fetch("/generate", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) }));
      if (token !== generationToken) return;
      currentGenerationId = data.id;
      if (!currentGenerationId) throw new Error("The server did not return a generation ID.");
      generate.textContent = "Generating...";
      renderStatus(data.status || "queued", data.progress);
      if (data.status === "completed") {
        active = false;
        renderVideo(currentGenerationId, data.output_url);
        generate.textContent = "Generate Video";
        updateGenerateEnabled();
      } else if (data.status === "failed") {
        active = false;
        setError(data.error || "Video generation failed.");
        generate.textContent = "Generate Video";
        updateGenerateEnabled();
      } else schedulePoll();
    }
    catch (error) { if (token !== generationToken) return; active = false; renderStatus("failed"); generate.textContent = "Generate Video"; setError(error.message || "OpenRouter rejected the generation request."); updateGenerateEnabled(); }
  }
  prompt.addEventListener("input", () => { const count = Array.from(prompt.value).length; promptCount.textContent = `${count} / ${maxPromptLength}`; if (count <= maxPromptLength) promptError.textContent = ""; else promptError.textContent = `Prompt must be ${maxPromptLength} characters or fewer.`; updateGenerateEnabled(); });
  model.addEventListener("change", updateOptions); model.addEventListener("change", updateGenerateEnabled); form.addEventListener("submit", submitGeneration); retryStatusButton.addEventListener("click", () => { setError(""); statusErrors = 0; retryStatusButton.hidden = true; if (currentGenerationId && active) pollGeneration(); });
  renderStatus("idle"); loadModels();
})();
