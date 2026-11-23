package web

import (
	"os"
	"strings"
	"testing"
)

// The Feishu card's behaviour is covered by webtest/tests/feishu-connect.test.mjs;
// this pins its wiring into the console.
func TestFeishuConnectCardIsWired(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `<script src="/app/feishu-connect.js"></script>`) {
		t.Fatal("index.html must load app/feishu-connect.js")
	}
	settings, err := os.ReadFile("app/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), "mountFeishuConnect(host);") {
		t.Fatal("personal settings must mount the Feishu card")
	}
}
