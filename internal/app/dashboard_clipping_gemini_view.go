package app

// enrichClippingGeminiJob adds only immutable request options and the safe
// durable lifecycle. Routing, credentials, source URLs and provider text stay
// out of dashboard and live-event payloads.
func (s *Store) enrichClippingGeminiJob(view *clippingJobView, job ClippingJob) error {
	if job.Provider != ClippingProviderGemini {
		return nil
	}
	snapshot, err := s.ClippingGeminiRequestSnapshot(job.ID)
	if err != nil {
		return err
	}
	if ValidateClippingGeminiOptions(snapshot.Options) != nil || snapshot.JobID != job.ID ||
		snapshot.Options.ContentType != job.ContentType || snapshot.Options.CandidateLimit != job.CandidateLimit {
		return ErrClippingInvalidState
	}
	lifecycle, err := s.ClippingGeminiLifecycle(job.ID, 0)
	if err != nil {
		return err
	}
	view.Options = &snapshot.Options
	view.ClippingGeminiLifecycleView = &lifecycle
	return nil
}
