package domain

import "fmt"

// BranchNotDeliverableError means the SOP delivery fallback found no branch to
// open an MR from: the work branch is absent on the remote or has no commits
// ahead of the target. It is not a failure — the caller records a readable run
// event and continues — so it is kept distinct from transport/auth errors.
// GitLab otherwise accepts an MR from a missing or empty source branch and
// produces an empty MR (no head SHA, zero changes).
type BranchNotDeliverableError struct {
	Project string
	Branch  string
	Target  string
	Reason  string
}

func (e *BranchNotDeliverableError) Error() string {
	return fmt.Sprintf("GitLab 仓库 %s 的工作分支 %s 无法开出 MR：%s", e.Project, e.Branch, e.Reason)
}
