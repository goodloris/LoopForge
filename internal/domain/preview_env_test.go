package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPreviewEnvIDIsStable(t *testing.T) {
	id := PreviewEnvID("issue-1", "repo-1")
	// "pe-" + hex(sha256("issue-1\x00repo-1"))[:10]
	if id != "pe-e26c2f6625" {
		t.Fatalf("PreviewEnvID = %q", id)
	}
	if PreviewEnvID(" issue-1 ", "repo-1") != id {
		t.Fatal("surrounding spaces must not change the id")
	}
	if PreviewEnvID("issue-1", "repo-2") == id || PreviewEnvID("issue-1repo-1", "") == id {
		t.Fatal("different inputs must give different ids")
	}
}

func TestPreviewContractCompose(t *testing.T) {
	c := &PreviewContract{Enabled: true, Kind: " Compose "}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if !c.Compose() || c.Static() || c.Kind != PreviewKindCompose || c.ComposeFile != PreviewDefaultComposeFile {
		t.Fatalf("normalized compose contract = %+v", c)
	}
	if c.SourcePath != "" || c.IndexFile != "" {
		t.Fatalf("compose must not gain static defaults: %+v", c)
	}
	custom := &PreviewContract{Enabled: true, Kind: "compose", ComposeFile: "/deploy\\preview.yaml"}
	if err := custom.Validate(); err != nil || custom.ComposeFile != "deploy/preview.yaml" {
		t.Fatalf("custom compose file = %q, %v", custom.ComposeFile, err)
	}
	for _, bad := range []string{"../x.yaml", "a/../../x.yaml", ".", "./", "/./", "C:", "c:/x.yaml", "D:\\x.yaml"} {
		if err := (&PreviewContract{Enabled: true, Kind: "compose", ComposeFile: bad}).Validate(); err == nil {
			t.Errorf("composeFile %q accepted", bad)
		}
	}
	for _, good := range []string{"./deploy/compose.yaml", "a.b/c:d.yaml", "/"} {
		if err := (&PreviewContract{Enabled: true, Kind: "compose", ComposeFile: good}).Validate(); err != nil {
			t.Errorf("composeFile %q rejected: %v", good, err)
		}
	}
	if err := (&PreviewContract{Enabled: true, Kind: "service"}).Validate(); err == nil {
		t.Error("unknown kind accepted")
	}
	wf, err := AttachPreviewBuild(WorkflowDefinition{Preview: &PreviewContract{Enabled: true, Kind: "compose"}})
	if err != nil || len(wf.Nodes) != 0 {
		t.Fatalf("compose must not inject the static build node: %v %+v", err, wf.Nodes)
	}
}

func TestPreviewContractStaticUnchanged(t *testing.T) {
	c := &PreviewContract{Enabled: true}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	want := PreviewContract{Enabled: true, Kind: PreviewKindStatic, SourcePath: PreviewDefaultSource, IndexFile: "index.html", RetentionSec: PreviewMaxRetentionSec}
	if *c != want || c.Compose() {
		t.Fatalf("static contract = %+v, want %+v", *c, want)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "composeFile") {
		t.Fatalf("static contract JSON gained composeFile: %s", raw)
	}
	if err := (&PreviewContract{Enabled: true, SourcePath: "../x"}).Validate(); err == nil {
		t.Error("static sourcePath escape accepted")
	}
	var nilContract *PreviewContract
	if nilContract.Compose() || nilContract.ComposeFileOrDefault() != PreviewDefaultComposeFile {
		t.Error("nil contract")
	}
}

func TestComposePreviewRunnerVersionAndPublicTask(t *testing.T) {
	if !(Runner{RunnerVersion: RunnerVersionComposePreview}).SupportsComposePreview() ||
		(Runner{RunnerVersion: RunnerVersionFeishu}).SupportsComposePreview() ||
		!versionAtLeast(RunnerVersionComposePreview, RunnerVersionFeishu) {
		t.Fatal("compose preview version gate")
	}
	if reason := (Runner{RunnerVersion: RunnerVersionFeishu}).ComposePreviewUnsupportedReason(); !strings.Contains(reason, RunnerVersionComposePreview) {
		t.Fatalf("upgrade hint = %q", reason)
	}
	if (Runner{RunnerVersion: RunnerVersionComposePreview}).ComposePreviewUnsupportedReason() != "" {
		t.Fatal("supported runner got an upgrade hint")
	}
	task := RunnerTask{Action: RunnerTaskActionPreviewDeploy, Preview: &RunnerPreviewSpec{EnvID: "pe-1", Env: map[string]string{"PREVIEW_ADMIN_PASSWORD": "x"}}}
	public := PublicRunnerTask(task)
	if public.Preview.Env != nil || task.Preview.Env == nil || public.Preview.EnvID != "pe-1" {
		t.Fatalf("public preview spec = %+v", public.Preview)
	}
	if (RunnerPreviewSpec{}).ComposeFileOrDefault() != PreviewDefaultComposeFile {
		t.Fatal("spec compose default")
	}
}
