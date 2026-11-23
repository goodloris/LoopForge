package tracker

import (
	"context"
	"log"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

const (
	mrDeliverySyncInterval = 5 * time.Minute
	mrDeliveryStaleAfter   = 5 * time.Minute
)

// MRDeliveryStore is the persistence surface used by the MR state background sync.
type MRDeliveryStore interface {
	PlatformGitHTTPSAddresses(context.Context, string) ([]netip.Addr, error)
	DiscoverMRDeliveryStatesFromArtifacts() error
	MRDeliveryStatesNeedingRefresh(staleBefore time.Time) ([]domain.MRDeliveryState, error)
	UpdateMRDeliveryState(project string, iid int, state string, targetBranch string, mergedAt, checkedAt time.Time) error
	ReconcileMergedMRDeliveries() error
	ReconcileCompletedDeliveries() error
	ReconcileUnlinkedDeliveryMRs() error
	ReconcileDeliveryMRStates() error
	Repositories() []domain.Repository
	EnsureMRDeliveryState(project string, iid int, url, issueID string) error
	MergedWorkMRs(project string) ([]domain.MRDeliveryState, error)
	ApplyDefaultBranchLanding(issueID, mrURL string, onDefault bool) error
}

// StartMRStateSync launches a background loop that discovers MR links and
// refreshes merge states from Gongfeng MCP into MySQL.
func (s *Service) StartMRStateSync(store MRDeliveryStore) {
	if s == nil || store == nil {
		return
	}
	s.autoMu.Lock()
	if s.mrTicker != nil {
		s.autoMu.Unlock()
		return
	}
	s.mrTicker = time.NewTicker(mrDeliverySyncInterval)
	s.mrStopCh = make(chan struct{})
	ticker, stopCh := s.mrTicker, s.mrStopCh
	s.autoMu.Unlock()

	go func() {
		s.syncMRDeliveryStates(store)
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				s.syncMRDeliveryStates(store)
			}
		}
	}()
}

// StopMRStateSync halts the MR delivery background sync.
func (s *Service) StopMRStateSync() {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	if s.mrTicker != nil {
		s.mrTicker.Stop()
		s.mrTicker = nil
	}
	if s.mrStopCh != nil {
		close(s.mrStopCh)
		s.mrStopCh = nil
	}
}

// SyncMRDeliveryStatesOnce runs discovery + refresh immediately (for tests/admin).
func (s *Service) SyncMRDeliveryStatesOnce(store MRDeliveryStore) {
	if s == nil || store == nil {
		return
	}
	s.syncMRDeliveryStates(store)
}

func (s *Service) syncMRDeliveryStates(store MRDeliveryStore) {
	if store == nil {
		return
	}
	s.deliveryReconcileOnce.Do(func() {
		if err := store.ReconcileMergedMRDeliveries(); err != nil {
			log.Printf("mr-delivery reconcile: %v", err)
		}
		if err := store.ReconcileCompletedDeliveries(); err != nil {
			log.Printf("delivery reconcile: %v", err)
		}
		if err := store.ReconcileUnlinkedDeliveryMRs(); err != nil {
			log.Printf("delivery unlinked-mr reconcile: %v", err)
		}
	})
	if err := store.ReconcileDeliveryMRStates(); err != nil {
		log.Printf("delivery mr-state reconcile: %v", err)
	}
	if err := store.DiscoverMRDeliveryStatesFromArtifacts(); err != nil {
		log.Printf("mr-delivery discover: %v", err)
		return
	}
	s.discoverPromoteMRs(store)
	s.syncWorkCommitsOnDefaultBranch(store)
	staleBefore := time.Now().UTC().Add(-mrDeliveryStaleAfter)
	pending, err := store.MRDeliveryStatesNeedingRefresh(staleBefore)
	if err != nil {
		log.Printf("mr-delivery list: %v", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	now := time.Now().UTC()
	updated := 0
	for _, row := range pending {
		if strings.HasPrefix(row.MRProject, "github:") {
			if ok, err := s.refreshExternalDelivery(store, row); ok || err != nil {
				if err != nil {
					log.Printf("GitHub delivery refresh: %v", err)
				} else {
					updated++
				}
				continue
			}
			if err := s.refreshGitHubDelivery(store, row.MRProject, row.MRIID); err != nil {
				log.Printf("GitHub delivery refresh: %v", err)
			} else {
				updated++
			}
			continue
		}
		if strings.HasPrefix(row.MRProject, domain.MRProjectGHE) || strings.HasPrefix(row.MRProject, domain.MRProjectGitLab) {
			if ok, err := s.refreshExternalDelivery(store, row); err != nil {
				log.Printf("mr-delivery refresh %s!%d: %v", row.MRProject, row.MRIID, err)
			} else if ok {
				updated++
			}
			continue
		}
		client, _, clientErr := s.gongfengClientForRemoteProject(row.MRProject)
		if clientErr != nil {
			log.Printf("mr-delivery auth %s: %v", row.MRProject, clientErr)
			continue
		}
		client = client.Clone(5 * time.Second)
		state, target, mergedAt := s.lookupMRState(client, row.MRProject, row.MRIID)
		if state == "" || state == "unknown" {
			// Failed lookups must not downgrade historical merged rows while
			// their missing timestamps are being refreshed.
			continue
		}
		if err := store.UpdateMRDeliveryState(row.MRProject, row.MRIID, state, target, mergedAt, now); err != nil {
			log.Printf("mr-delivery update %s!%d: %v", row.MRProject, row.MRIID, err)
			continue
		}
		updated++
	}
	if updated > 0 {
		log.Printf("mr-delivery sync: refreshed %d/%d MR states", updated, len(pending))
	}
}

func (s *Service) discoverPromoteMRs(store MRDeliveryStore) {
	if s == nil || store == nil {
		return
	}
	now := time.Now().UTC()
	for _, repo := range store.Repositories() {
		project := strings.TrimSpace(repo.GongfengProjectID)
		if strings.HasPrefix(project, "github:") {
			continue
		}
		def := strings.TrimSpace(repo.DefaultBranch)
		integ := domain.NormalizeIntegrateBranch(repo.IntegrateBranch)
		if project == "" || def == "" || integ == "" || domain.SameGitBranch(integ, def) {
			continue
		}
		client, _, clientErr := s.trackerClientForProject("gongfeng", repo.ProjectID)
		if clientErr != nil {
			log.Printf("mr-delivery auth %s: %v", repo.ProjectID, clientErr)
			continue
		}
		client = client.Clone(15 * time.Second)
		for _, state := range []string{"opened", "merged"} {
			raw, err := client.CallTool("search_merge_request", map[string]any{
				"project_id":    project,
				"source_branch": integ,
				"target_branch": def,
				"state":         state,
				"per_page":      8,
				"order_by":      "updated_at",
				"sort":          "desc",
			})
			if err != nil {
				log.Printf("mr-delivery discover promote %s %s: %v", project, state, err)
				continue
			}
			for _, mr := range parseGongfengMRRecords(raw) {
				if mr.IID <= 0 {
					continue
				}
				url := domain.GongfengMRURL(project, mr.IID)
				if err := store.EnsureMRDeliveryState(project, mr.IID, url, domain.BaselinePromoteIssueID); err != nil {
					log.Printf("mr-delivery ensure promote %s!%d: %v", project, mr.IID, err)
					continue
				}
				st := normalizeMRState(mr.State)
				if st == "" {
					st = state
				}
				target := strings.TrimSpace(mr.TargetBranch)
				if target == "" {
					target = def
				}
				mr.State = st
				if err := store.UpdateMRDeliveryState(project, mr.IID, st, target, gongfengMRMergedAt(mr), now); err != nil {
					log.Printf("mr-delivery update promote %s!%d: %v", project, mr.IID, err)
				}
			}
		}
	}
}

// syncWorkCommitsOnDefaultBranch marks a baseline MR as on the default branch
// only when its source commit is contained in that branch. A missing or failed
// lookup leaves the current label unchanged.
func (s *Service) syncWorkCommitsOnDefaultBranch(store MRDeliveryStore) {
	if s == nil || store == nil {
		return
	}
	for _, repo := range store.Repositories() {
		project := strings.TrimSpace(repo.GongfengProjectID)
		def := strings.TrimSpace(repo.DefaultBranch)
		if project == "" || def == "" {
			continue
		}
		if strings.HasPrefix(project, "github:") {
			s.syncGitHubWorkCommitsOnDefaultBranch(store, project, def, domain.NormalizeIntegrateBranch(repo.IntegrateBranch))
			continue
		}
		rows, err := store.MergedWorkMRs(project)
		if err != nil {
			log.Printf("mr-delivery work commits %s: %v", project, err)
			continue
		}
		client, _, clientErr := s.trackerClientForProject("gongfeng", repo.ProjectID)
		if clientErr != nil {
			log.Printf("mr-delivery work commits auth %s: %v", repo.ProjectID, clientErr)
			continue
		}
		client = client.Clone(15 * time.Second)
		for _, row := range rows {
			if row.MRIID <= 0 || domain.IsBaselinePromoteIssueID(row.IssueID) {
				continue
			}
			if domain.IsMainlineBranch(row.TargetBranch, def) {
				continue
			}
			cacheKey := project + "#" + strconv.Itoa(row.MRIID)
			if _, seen := s.landedOnDefault.Load(cacheKey); seen {
				continue
			}
			onDefault, ok := workCommitOnBranch(client, project, row.MRIID, def)
			if !ok {
				continue
			}
			if err := store.ApplyDefaultBranchLanding(row.IssueID, row.MRURL, onDefault); err != nil {
				log.Printf("mr-delivery landing %s!%d: %v", project, row.MRIID, err)
				continue
			}
			if onDefault {
				s.landedOnDefault.Store(cacheKey, true)
			}
		}
	}
}

func workCommitOnBranch(client *MCPClient, project string, iid int, branch string) (onDefault bool, ok bool) {
	if client == nil || iid <= 0 || strings.TrimSpace(branch) == "" {
		return false, false
	}
	raw, err := client.CallTool("search_merge_request", map[string]any{
		"project_id": project,
		"iid":        iid,
		"per_page":   1,
	})
	if err != nil {
		log.Printf("mr-delivery source commit %s!%d: %v", project, iid, err)
		return false, false
	}
	sha := parseGongfengSourceCommit(raw, iid)
	if sha == "" {
		return false, false
	}
	const pageSize = 100
	for page := 1; page <= 20; page++ {
		refsRaw, err := client.CallTool("get_commit_refs", map[string]any{
			"project_id": project,
			"commit_sha": sha,
			"type":       "branch",
			"page":       page,
			"per_page":   pageSize,
		})
		if err != nil {
			log.Printf("mr-delivery commit refs %s %s: %v", project, sha, err)
			return false, false
		}
		names, parsed := parseGongfengRefNames(refsRaw)
		if !parsed {
			return false, false
		}
		if domain.BranchListed(names, branch) {
			return true, true
		}
		if len(names) < pageSize {
			return false, true
		}
	}
	// The ref list was truncated before the default branch could be ruled out.
	return false, false
}

// syncGitHubWorkCommitsOnDefaultBranch is the GitHub side of
// syncWorkCommitsOnDefaultBranch: a pull merged into the integrate branch
// counts as delivered once its head commit is contained in the default branch
// (the integrate branch was promoted).
func (s *Service) syncGitHubWorkCommitsOnDefaultBranch(store MRDeliveryStore, project, def, integ string) {
	rows, err := store.MergedWorkMRs(project)
	if err != nil {
		log.Printf("mr-delivery work commits %s: %v", project, err)
		return
	}
	var g *githubClient
	promotesListed := false
	for _, row := range rows {
		if row.MRIID <= 0 || domain.IsBaselinePromoteIssueID(row.IssueID) || domain.IsMainlineBranch(row.TargetBranch, def) {
			continue
		}
		cacheKey := project + "#" + strconv.Itoa(row.MRIID)
		if _, seen := s.landedOnDefault.Load(cacheKey); seen {
			continue
		}
		if g == nil {
			if g, err = s.githubClient(project); err != nil {
				log.Printf("mr-delivery work commits %s: %v", project, err)
				return
			}
		}
		if !promotesListed {
			// Promote PRs date the landing (see ApplyDefaultBranchLanding).
			promotesListed = true
			s.recordGitHubPromotePulls(store, g, project, integ, def)
		}
		onDefault, ok := g.pullOnBranch(row.MRIID, def)
		if !ok {
			continue
		}
		if err := store.ApplyDefaultBranchLanding(row.IssueID, row.MRURL, onDefault); err != nil {
			log.Printf("mr-delivery landing %s#%d: %v", project, row.MRIID, err)
			continue
		}
		if onDefault {
			s.landedOnDefault.Store(cacheKey, true)
		}
	}
}

// recordGitHubPromotePulls stores merged integrate → default pulls as
// baseline promote rows, like discoverPromoteMRs does for Gongfeng.
func (s *Service) recordGitHubPromotePulls(store MRDeliveryStore, g *githubClient, project, integ, def string) {
	if integ == "" || domain.SameGitBranch(integ, def) {
		return
	}
	pulls, err := g.listPulls(integ, def)
	if err != nil {
		log.Printf("mr-delivery discover promote %s: %v", project, err)
		return
	}
	now := time.Now().UTC()
	for _, p := range pulls {
		if !p.merged() || p.Head.Ref != integ || p.Base.Ref != def {
			continue
		}
		if err := store.EnsureMRDeliveryState(project, p.Number, domain.GongfengMRURL(project, p.Number), domain.BaselinePromoteIssueID); err != nil {
			log.Printf("mr-delivery ensure promote %s#%d: %v", project, p.Number, err)
			continue
		}
		if err := store.UpdateMRDeliveryState(project, p.Number, "merged", def, p.mergedAt(), now); err != nil {
			log.Printf("mr-delivery update promote %s#%d: %v", project, p.Number, err)
		}
	}
}

// pullOnBranch reports whether the pull's head commit is contained in branch.
// ok is false when GitHub could not answer.
func (g *githubClient) pullOnBranch(number int, branch string) (onBranch, ok bool) {
	var p githubPull
	if err := g.request("GET", "/pulls/"+strconv.Itoa(number), nil, &p); err != nil {
		log.Printf("mr-delivery pull %s#%d: %v", g.path, number, err)
		return false, false
	}
	if strings.TrimSpace(p.Head.SHA) == "" {
		return false, false
	}
	var cmp struct {
		Status string `json:"status"`
	}
	if err := g.request("GET", "/compare/"+branch+"..."+p.Head.SHA, nil, &cmp); err != nil {
		log.Printf("mr-delivery compare %s %s...%s: %v", g.path, branch, p.Head.SHA, err)
		return false, false
	}
	// head is an ancestor of (or equal to) branch.
	return cmp.Status == "behind" || cmp.Status == "identical", true
}
