package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"

	_ "github.com/glebarez/go-sqlite"
)

// kiroDataDirEnv names a kiro-cli data directory holding the login to copy
// when this process does not run on the machine that logged in: the platform
// container mounts the host's read-only (compose.yaml).
const kiroDataDirEnv = "AGENTFLOW_KIRO_DATA_DIR"

// kiroCopyDataRel is where a Linux kiro-cli looks for its database under
// HOME; Docker sandboxes and the platform container are Linux.
const kiroCopyDataRel = ".local/share/kiro-cli/data.sqlite3"

// kiroHostDataRel is where this OS's kiro-cli keeps its database under HOME.
func kiroHostDataRel() string {
	if goruntime.GOOS == "darwin" {
		return "Library/Application Support/kiro-cli/data.sqlite3"
	}
	return kiroCopyDataRel
}

// kiroLoginDatabase is the kiro-cli database whose login this process can
// use: AGENTFLOW_KIRO_DATA_DIR, else this machine's own data directory.
func kiroLoginDatabase() string {
	if dir := strings.TrimSpace(os.Getenv(kiroDataDirEnv)); dir != "" {
		return filepath.Join(dir, "data.sqlite3")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	if goruntime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "kiro-cli", "data.sqlite3")
	}
	if data := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); data != "" {
		return filepath.Join(data, "kiro-cli", "data.sqlite3")
	}
	return filepath.Join(home, ".local", "share", "kiro-cli", "data.sqlite3")
}

// KiroDataDirWarning returns a startup warning when AGENTFLOW_KIRO_DATA_DIR is
// set but holds no data.sqlite3 (for example an empty fallback mount), else "".
func KiroDataDirWarning() string {
	dir := strings.TrimSpace(os.Getenv(kiroDataDirEnv))
	if dir == "" || KiroLoginAvailable() {
		return ""
	}
	warning := kiroDataDirEnv + "=" + dir + " contains no data.sqlite3: Kiro CLI will show as unconfigured."
	// /kiro-host is the compose.yaml mount; elsewhere the variable was set by hand.
	if dir == "/kiro-host" {
		warning += " Set LOOPFORGE_KIRO_DATA_DIR in .env to the host's kiro-cli data directory and recreate the container (docker compose up -d)."
	}
	return warning
}

// KiroLoginAvailable reports whether this process has a kiro-cli login to
// run Kiro CLI on. It does not check that the login is still valid.
func KiroLoginAvailable() bool {
	path := kiroLoginDatabase()
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// copyKiroLogin writes a kiro-cli database into home that holds only the
// login (auth_kv kirocli:odic:*) and the schema: no conversations, history or
// state of the machine. The copy refreshes its own access token; the refresh
// token does not rotate, so the original keeps working (verified against
// kiro-cli 3.3.0 with IAM Identity Center).
func copyKiroLogin(home AgentCLIHome, rel string) error {
	source := kiroLoginDatabase()
	if source == "" || !KiroLoginAvailable() {
		return errors.New("Kiro CLI 未登录：请在执行机器上运行 kiro-cli login（订阅登录）")
	}
	data, err := kiroLoginSnapshot(source)
	if err != nil {
		return fmt.Errorf("复制 Kiro CLI 登录失败：%w", err)
	}
	return writeAgentCLIFile(home, rel, data)
}

// kiroLoginSnapshot reads source through SQLite (consistent under its lock
// while kiro-cli writes) and returns the stripped database file.
func kiroLoginSnapshot(source string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "loopforge-kiro-login-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	copyPath := filepath.Join(dir, "data.sqlite3")
	src, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: source, RawQuery: "mode=ro"}).String())
	if err != nil {
		return nil, err
	}
	_, err = src.Exec("VACUUM INTO ?", copyPath)
	src.Close()
	if err != nil {
		return nil, err
	}
	out, err := sql.Open("sqlite", copyPath)
	if err != nil {
		return nil, err
	}
	defer out.Close()
	rows, err := out.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT IN ('auth_kv','migrations','state') AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, name)
	}
	rows.Close()
	for _, table := range tables {
		if _, err := out.Exec(`DELETE FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`); err != nil {
			return nil, err
		}
	}
	var logins int
	if err := out.QueryRow(`SELECT count(*) FROM auth_kv WHERE key LIKE 'kirocli:odic:%'`).Scan(&logins); err != nil {
		return nil, err
	}
	if logins == 0 {
		return nil, errors.New("Kiro CLI 未登录：请在执行机器上运行 kiro-cli login（订阅登录）")
	}
	if _, err := out.Exec(`DELETE FROM auth_kv WHERE key NOT LIKE 'kirocli:odic:%'`); err != nil {
		return nil, err
	}
	// The login's profile and Identity Center settings live in state; a
	// macOS kiro-cli refuses the token without the selected profile.
	if _, err := out.Exec(`DELETE FROM state WHERE key NOT IN ('api.codewhisperer.profile','auth.idc.start-url','auth.idc.region')`); err != nil && !strings.Contains(err.Error(), "no such table") {
		return nil, err
	}
	if _, err := out.Exec(`VACUUM`); err != nil {
		return nil, err
	}
	out.Close()
	return os.ReadFile(copyPath)
}

// The pinned Linux kiro-cli the image bundles (Dockerfile) and a workspace
// image without it installs. The release publishes these SHA-256 sums next
// to each zip.
const (
	KiroCLIVersion      = "3.4.0"
	kiroCLISHA256AMD64  = "95b85a724ecda5340cc5c98ddc979f4d0a6e96712fb29a36b3e7afaf6c70d389"
	kiroCLISHA256ARM64  = "98401395ca36eda9682445062c090b2c81413640d9f1e7433e741fa3147dde6e"
	kiroCLIDownloadBase = "https://desktop-release.q.us-east-1.amazonaws.com/"
)

// kiroInstallScript installs the pinned kiro-cli into binDir, a shell word
// the caller quotes, after checking its SHA-256. python3 (which the native
// approval hooks need anyway) downloads and unpacks it, since images may lack
// curl and unzip.
func kiroInstallScript(binDir string) string {
	return `case "$(uname -m)" in
  x86_64|amd64) kiro_arch=x86_64 kiro_sum=` + kiroCLISHA256AMD64 + ` ;;
  aarch64|arm64) kiro_arch=aarch64 kiro_sum=` + kiroCLISHA256ARM64 + ` ;;
  *) echo "Kiro CLI 不支持的架构 $(uname -m)" >&2; exit 1 ;;
esac
mkdir -p ` + binDir + `
python3 - "` + kiroCLIDownloadBase + KiroCLIVersion + `/kirocli-${kiro_arch}-linux.zip" "$kiro_sum" ` + binDir + `/kiro-cli <<'PY'
import hashlib, io, os, sys, urllib.request, zipfile
url, want, dest = sys.argv[1:4]
data = urllib.request.urlopen(url, timeout=600).read()
if hashlib.sha256(data).hexdigest() != want:
    sys.exit("kiro-cli download failed its SHA-256 check")
tmp = dest + ".part"
with open(tmp, "wb") as out:
    out.write(zipfile.ZipFile(io.BytesIO(data)).read("kirocli/bin/kiro-cli"))
os.chmod(tmp, 0o755)
os.replace(tmp, dest)
PY`
}

var kiroCacheMu sync.Mutex

// kiroCachedCLI is where EnsureKiroCLICache keeps the Linux kiro-cli that
// Docker sandboxes run from the read-only RuntimeCLIDir mount.
func kiroCachedCLI(root string) string {
	return filepath.Join(root, "kiro-cli", KiroCLIVersion, "linux-"+goruntime.GOARCH, "kiro-cli")
}

// EnsureKiroCLICache downloads the pinned Linux kiro-cli for this machine's
// architecture into RuntimeCLIDir once (sandboxes share the host's), checks
// its SHA-256 and returns the binary path, valid on the host and in sandboxes.
func EnsureKiroCLICache(ctx context.Context) (string, error) {
	root := RuntimeCLIDir()
	if root == "" {
		return "", errors.New("AGENTFLOW_RUNTIME_CLI_DIR 未配置")
	}
	sums := map[string]struct{ arch, sum string }{"amd64": {"x86_64", kiroCLISHA256AMD64}, "arm64": {"aarch64", kiroCLISHA256ARM64}}
	want, ok := sums[goruntime.GOARCH]
	if !ok {
		return "", fmt.Errorf("Kiro CLI 不支持的架构 %s", goruntime.GOARCH)
	}
	binary := kiroCachedCLI(root)
	if fileIsExecutable(binary) {
		return binary, nil
	}
	kiroCacheMu.Lock()
	defer kiroCacheMu.Unlock()
	if fileIsExecutable(binary) {
		return binary, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kiroCLIDownloadBase+KiroCLIVersion+"/kirocli-"+want.arch+"-linux.zip", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载 Kiro CLI 失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 Kiro CLI 失败：HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return "", fmt.Errorf("下载 Kiro CLI 失败：%w", err)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want.sum {
		return "", errors.New("Kiro CLI 下载未通过 SHA-256 校验")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	entry, err := archive.Open("kirocli/bin/kiro-cli")
	if err != nil {
		return "", err
	}
	defer entry.Close()
	// Sandboxes read the cache through a read-only bind mount as any user.
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		return "", err
	}
	stage, err := os.CreateTemp(filepath.Dir(binary), ".kiro-cli-")
	if err != nil {
		return "", err
	}
	defer os.Remove(stage.Name())
	_, err = io.Copy(stage, entry)
	if closeErr := stage.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err := os.Chmod(stage.Name(), 0o755); err != nil {
		return "", err
	}
	return binary, os.Rename(stage.Name(), binary)
}

var kiroRuntimeMu sync.Mutex

// ensureKiroHostRuntime is EnsureKiroHostRuntime, replaceable in tests.
var ensureKiroHostRuntime = EnsureKiroHostRuntime

// EnsureKiroHostRuntime unpacks this machine's kiro-cli runtime (about 415
// MB) once into a shared cache and returns it as a KIRO_RUNTIME_DIR that
// process sandboxes read but never write. The cache is keyed by the
// installed kiro-cli version, since each release bundles its own runtime.
func EnsureKiroHostRuntime(ctx context.Context) (string, error) {
	binary, err := exec.LookPath("kiro-cli")
	if err != nil {
		home, _ := os.UserHomeDir()
		if candidate := filepath.Join(home, ".local", "bin", "kiro-cli"); fileIsExecutable(candidate) {
			binary, err = candidate, nil
		}
	}
	if err != nil {
		return "", errors.New("kiro-cli 未安装")
	}
	out, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("kiro-cli --version: %w", err)
	}
	cacheRoot := RuntimeCLIDir()
	if cacheRoot == "" {
		if cacheRoot, err = os.UserCacheDir(); err != nil {
			return "", err
		}
		cacheRoot = filepath.Join(cacheRoot, "loopforge")
	}
	sum := sha256.Sum256(append(out, []byte(goruntime.GOOS+"/"+goruntime.GOARCH)...))
	dir := filepath.Join(cacheRoot, "kiro-runtime", hex.EncodeToString(sum[:8]))
	if _, err := os.Stat(filepath.Join(dir, ".ready")); err == nil {
		return dir, nil
	}
	kiroRuntimeMu.Lock()
	defer kiroRuntimeMu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, ".ready")); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(filepath.Dir(dir), ".unpack-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	scratch, err := os.MkdirTemp("", "loopforge-kiro-unpack-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch)
	// kiro-cli unpacks its runtime before it authenticates; the invalid key
	// keeps the machine's login out of this one-off run.
	unpack := exec.CommandContext(ctx, binary, "chat", "--v3", "--no-interactive", "hi")
	unpack.Env = append(os.Environ(), "HOME="+scratch, "KIRO_HOME="+filepath.Join(scratch, ".kiro"), "XDG_DATA_HOME="+filepath.Join(scratch, "data"), "KIRO_RUNTIME_DIR="+stage, "KIRO_API_KEY=loopforge-runtime-unpack", "KIRO_NO_AUTO_UPDATE=1")
	// The run itself fails at sign-in, so its error says nothing; a
	// complete runtime is a version directory with tui.js and kas.
	_ = unpack.Run()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	entries, _ := os.ReadDir(stage)
	found := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		_, tui := os.Stat(filepath.Join(stage, entry.Name(), "tui.js"))
		_, kas := os.Stat(filepath.Join(stage, entry.Name(), "kas", "node_modules"))
		found = found || (tui == nil && kas == nil)
	}
	if !found {
		return "", errors.New("kiro-cli 没有解压出完整的运行时")
	}
	if err := os.WriteFile(filepath.Join(stage, ".ready"), nil, 0o644); err != nil {
		return "", err
	}
	if err := filepath.Walk(stage, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chmod(path, info.Mode()|0o444|map[bool]os.FileMode{true: 0o111}[info.IsDir()])
	}); err != nil {
		return "", err
	}
	if err := os.Rename(stage, dir); err != nil && !os.IsExist(err) {
		return "", err
	}
	return dir, nil
}
