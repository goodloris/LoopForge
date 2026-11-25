package domain

import (
	"errors"
	"fmt"
)

// ErrMergeQueuedForPipeline reports that a merge request was set to merge
// automatically when its pipeline succeeds (GitLab
// merge_when_pipeline_succeeds), instead of merging now. Callers treat this
// as neither merged nor failed: the merge completes on the host when its
// pipeline passes, and the existing MR delivery-state polling
// (refreshExternalDelivery / refreshGitLabDelivery) advances it. It exists so
// a CI-gated project (only_allow_merge_if_pipeline_succeeds) does not fail the
// integration step while its pipeline runs.
var ErrMergeQueuedForPipeline = errors.New("merge request queued to merge when its pipeline succeeds")

// MergeQueuedError carries the queued MR reference and its pipeline link. It
// unwraps to ErrMergeQueuedForPipeline so callers match with errors.Is.
type MergeQueuedError struct {
	Ref         string
	PipelineURL string
}

func (e *MergeQueuedError) Error() string {
	if e.PipelineURL != "" {
		return fmt.Sprintf("merge request %s queued to merge when its pipeline succeeds: %s", e.Ref, e.PipelineURL)
	}
	return fmt.Sprintf("merge request %s queued to merge when its pipeline succeeds", e.Ref)
}

func (e *MergeQueuedError) Unwrap() error { return ErrMergeQueuedForPipeline }
