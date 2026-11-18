package repo

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	DropTestSchemas()
	os.Exit(code)
}
