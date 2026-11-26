package web

import (
	"os/exec"
	"testing"
)

func TestPreviewEnvCardsInteraction(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "preview_envs_test.js").CombinedOutput(); err != nil {
		t.Fatalf("preview env cards interaction: %v\n%s", err, out)
	}
}
