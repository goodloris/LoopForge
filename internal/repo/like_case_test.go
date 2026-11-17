package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// MySQL (utf8mb4_unicode_ci) and sqlite match LIKE without regard to case;
// PostgreSQL does not. Free text people name, such as artifact names, keeps
// the MySQL behaviour on every dialect (likeCI): an artifact a role named
// "MR-Summary" carries the MR link like "mr-summary" would, exactly as
// domain.MRArtifactPriority judges names.
func TestMRLinksInArtifactNamesAreFoundWithoutRegardToCase(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	run := domain.Run{ID: "like-ci-run", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}, Artifacts: []domain.Artifact{
		{NodeID: "deliver", Name: "MR-Summary", Content: "https://git.example.com/org/repo/-/merge_requests/41\n", Attempt: 1, CreatedAt: now},
		{NodeID: "mr", Name: "Fix.PATCH", Content: "https://git.example.com/org/repo/-/merge_requests/42\n", Attempt: 1, CreatedAt: now},
	}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscoverMRDeliveryStatesFromArtifacts(); err != nil {
		t.Fatal(err)
	}
	states, err := s.ListMRDeliveryStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].MRIID != 41 {
		t.Fatalf("MR links found in artifacts = %+v, want only !41 from MR-Summary", states)
	}
}

// IDs, tokens and paths match exactly. MessageWithAttachment narrows rows with
// LIKE (case-insensitive on MySQL and sqlite, case-sensitive on PostgreSQL)
// and then compares the attachment ID exactly, so every dialect agrees.
func TestAttachmentIDsMatchWithCase(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	room := domain.ChatRoom{ID: "like-cs-room", ProjectID: "platform", Name: "Case", CreatedBy: 7, CreatedAt: time.Now().UTC()}
	if err := s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMessage(domain.ChatMessage{ID: "m1", RoomID: room.ID, Kind: "text", Body: "file", Attachments: []domain.AssistantAttachment{{ID: "att-Ab12"}}, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.MessageWithAttachment(room.ID, "att-Ab12"); !ok || got.ID != "m1" {
		t.Fatalf("exact attachment ID not found: %+v %v", got, ok)
	}
	for _, id := range []string{"att-ab12", "ATT-AB12"} {
		if got, ok := s.MessageWithAttachment(room.ID, id); ok {
			t.Fatalf("attachment %q matched %q: %+v", id, "att-Ab12", got)
		}
	}
}
