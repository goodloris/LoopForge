package web

import (
	"os/exec"
	"testing"
)

func TestFlowPreviewKindSwitch(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "flow_preview_kind_test.js").CombinedOutput(); err != nil {
		t.Fatalf("flow preview kind switch: %v\n%s", err, out)
	}
}
