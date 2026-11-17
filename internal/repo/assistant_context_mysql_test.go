package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
	"testing"
)

// Exercise the production MySQL and PostgreSQL JSON projection without
// writing any rows.
func TestMySQLAssistantParticipantProjectionReadOnly(t *testing.T) {
	st := serverTestStore(t)
	r := st.AssistantRepo
	payload := `{"messages":[{"role":"user","authorId":11,"authorName":"alice","content":"hello"},{"role":"assistant","authorId":999,"content":"AI"},{"role":"user","authorId":12,"authorName":"bob","content":"world"},{"role":"user","content":"legacy"}]}`
	jsonType := "JSON"
	if st.isPostgres() {
		jsonType = "jsonb"
	}
	var result string
	if err := st.DB.Raw("SELECT ("+r.assistantParticipantSummarySQL()+") FROM (SELECT CAST(? AS "+jsonType+") AS payload) AS legacy", payload).Scan(&result).Error; err != nil {
		t.Fatal(err)
	}
	var messages []domain.AssistantMessage
	if err := json.Unmarshal([]byte(result), &messages); err != nil {
		t.Fatal(err, result)
	}
	s := domain.AssistantSession{Shared: true}
	deriveAssistantParticipants(&s, messages)
	if len(s.Participants) != 2 || !s.UnknownParticipants {
		t.Fatalf("wrong projection: %+v", s)
	}
}
