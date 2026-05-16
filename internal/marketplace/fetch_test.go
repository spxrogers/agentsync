package marketplace_test

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spxrogers/agentsync/internal/marketplace"
)

// ---- helpers ----------------------------------------------------------------

// makeWorkRepo initialises a non-bare git repo at dir, writes file at relPath
// with the given content, and commits. Returns the repo.
func makeWorkRepo(t *testing.T, dir, relPath, content string) *gogit.Repository {
	t.Helper()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, relPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, relPath), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	sig := &object.Signature{Name: "test", Email: "test@test", When: time.Now()}
	if _, err := w.Commit("init", &gogit.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return repo
}

// makeNPMTarball builds an in-memory npm-style .tgz with one file.
func makeNPMTarball(t *testing.T, fileName, content string) []byte {
	t.Helper()
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "pkg.tgz")
	f, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	data := []byte(content)
	hdr := &tar.Header{
		Name:    "package/" + fileName,
		Size:    int64(len(data)),
		Mode:    0o644,
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ---- RelativeFetcher tests --------------------------------------------------

func TestRelativeFetcher_CopiesDirectory(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "a.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	source := marketplace.Source{Relative: src}
	fetcher := marketplace.Dispatch(source)
	result, err := fetcher.Fetch(source, dst)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.HeadSHA != "" {
		t.Errorf("unexpected HeadSHA for relative: %s", result.HeadSHA)
	}

	data, err := os.ReadFile(filepath.Join(dst, "README.md"))
	if err != nil {
		t.Fatalf("README.md missing: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("README.md content = %q", data)
	}
	data2, err := os.ReadFile(filepath.Join(dst, "sub", "a.txt"))
	if err != nil {
		t.Fatalf("sub/a.txt missing: %v", err)
	}
	if string(data2) != "world" {
		t.Errorf("sub/a.txt content = %q", data2)
	}
}

func TestRelativeFetcher_RejectsPathTraversal(t *testing.T) {
	// Marketplace cache root holds a benign plugin layout; a marketplace
	// entry with `"source": "../escape"` is what we want to reject.
	mpRoot := t.TempDir()
	// Sibling directory the attacker would target.
	sibling := t.TempDir()
	if err := os.WriteFile(filepath.Join(sibling, "secret.txt"), []byte("you should not see this"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	src := marketplace.Source{
		Relative: sibling, // already resolved absolute, but outside RootDir
		RootDir:  mpRoot,
	}
	fetcher := marketplace.Dispatch(src)
	_, err := fetcher.Fetch(src, dst)
	if err == nil {
		t.Fatal("expected error for source outside RootDir")
	}
	// The attacker file must not have been copied.
	if _, statErr := os.Stat(filepath.Join(dst, "secret.txt")); statErr == nil {
		t.Fatalf("traversal succeeded — secret.txt copied into dst")
	}
}

func TestRelativeFetcher_AllowsContainedPath(t *testing.T) {
	mpRoot := t.TempDir()
	pluginDir := filepath.Join(mpRoot, "plugins", "demo")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "README.md"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	src := marketplace.Source{Relative: pluginDir, RootDir: mpRoot}
	fetcher := marketplace.Dispatch(src)
	if _, err := fetcher.Fetch(src, dst); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "README.md")); err != nil {
		t.Fatalf("README.md missing: %v", err)
	}
}

func TestRelativeFetcher_EmptyPath_Error(t *testing.T) {
	dst := t.TempDir()
	source := marketplace.Source{Relative: ""}
	fetcher := marketplace.Dispatch(source) // will error because relative="" and kind=""
	_, err := fetcher.Fetch(source, dst)
	if err == nil {
		t.Fatal("expected error for empty source")
	}
}

func TestDispatch_UnknownKind_Error(t *testing.T) {
	src := marketplace.Source{Kind: "bogus"}
	fetcher := marketplace.Dispatch(src)
	_, err := fetcher.Fetch(src, t.TempDir())
	if err == nil {
		t.Fatal("expected error for unknown source kind")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should mention kind: %v", err)
	}
}

// ---- GitFetcher tests -------------------------------------------------------

func TestGitFetcher_FileURL(t *testing.T) {
	workDir := t.TempDir()
	makeWorkRepo(t, workDir, "hello.txt", "from git")

	dst := t.TempDir()
	src := marketplace.Source{Kind: "url", Repo: "file://" + workDir}
	fetcher := marketplace.Dispatch(src)
	result, err := fetcher.Fetch(src, dst)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.HeadSHA == "" {
		t.Error("expected non-empty HeadSHA")
	}

	data, err := os.ReadFile(filepath.Join(dst, "hello.txt"))
	if err != nil {
		t.Fatalf("hello.txt not found: %v", err)
	}
	if string(data) != "from git" {
		t.Errorf("content = %q", data)
	}
}

func TestGitFetcher_Idempotent(t *testing.T) {
	workDir := t.TempDir()
	makeWorkRepo(t, workDir, "f.txt", "v1")

	dst := t.TempDir()
	src := marketplace.Source{Kind: "url", Repo: "file://" + workDir}
	fetcher := marketplace.Dispatch(src)

	r1, err := fetcher.Fetch(src, dst)
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	r2, err := fetcher.Fetch(src, dst)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if r1.HeadSHA != r2.HeadSHA {
		t.Errorf("sha mismatch: %s vs %s", r1.HeadSHA, r2.HeadSHA)
	}
}

func TestGitFetcher_GitSubdir(t *testing.T) {
	workDir := t.TempDir()
	repo, err := gogit.PlainInit(workDir, false)
	if err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(workDir, "tools", "myplugin")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "plugin.json"), []byte(`{"name":"myplugin"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "root.txt"), []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, _ := repo.Worktree()
	if _, err := w.Add("."); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	sig := &object.Signature{Name: "t", Email: "t@t", When: time.Now()}
	if _, err := w.Commit("init", &gogit.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	dst := t.TempDir()
	src := marketplace.Source{
		Kind: "git-subdir",
		Repo: "file://" + workDir,
		Path: "tools/myplugin",
	}
	fetcher := marketplace.Dispatch(src)
	result, err := fetcher.Fetch(src, dst)
	if err != nil {
		t.Fatalf("Fetch git-subdir: %v", err)
	}
	if result.HeadSHA == "" {
		t.Error("expected HeadSHA")
	}

	if _, err := os.Stat(filepath.Join(dst, "plugin.json")); err != nil {
		t.Fatalf("plugin.json missing after subdir extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "root.txt")); err == nil {
		t.Fatal("root.txt should have been excluded by git-subdir")
	}
}

// ---- NPMFetcher tests -------------------------------------------------------

func TestNPMFetcher_FakeRegistry(t *testing.T) {
	tarball := makeNPMTarball(t, "index.js", "console.log('hello')")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/@myorg/mypkg":
			meta := map[string]any{
				"versions": map[string]any{
					"1.0.0": map[string]any{
						"version": "1.0.0",
						"dist": map[string]any{
							"tarball": "http://" + r.Host + "/tarball/1.0.0",
						},
					},
				},
				"dist-tags": map[string]any{"latest": "1.0.0"},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(meta)
		case strings.HasPrefix(r.URL.Path, "/tarball/"):
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(tarball)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dst := t.TempDir()
	src := marketplace.Source{
		Kind:     "npm",
		Package:  "@myorg/mypkg",
		Version:  "1.0.0",
		Registry: srv.URL,
	}
	// NPMFetcher is exported; inject the test server's client.
	fetcher := &marketplace.NPMFetcher{HTTPClient: srv.Client()}
	result, err := fetcher.Fetch(src, dst)
	if err != nil {
		t.Fatalf("NPM Fetch: %v", err)
	}
	if result.Version != "1.0.0" {
		t.Errorf("version = %q", result.Version)
	}

	data, err := os.ReadFile(filepath.Join(dst, "index.js"))
	if err != nil {
		t.Fatalf("index.js missing: %v", err)
	}
	if string(data) != "console.log('hello')" {
		t.Errorf("content = %q", data)
	}
}

func TestNPMFetcher_LatestVersion(t *testing.T) {
	tarball := makeNPMTarball(t, "main.js", "// v2")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/mypkg":
			meta := map[string]any{
				"versions": map[string]any{
					"2.0.0": map[string]any{
						"version": "2.0.0",
						"dist": map[string]any{
							"tarball": "http://" + r.Host + "/tarball/2.0.0",
						},
					},
				},
				"dist-tags": map[string]any{"latest": "2.0.0"},
			}
			_ = json.NewEncoder(w).Encode(meta)
		case strings.HasPrefix(r.URL.Path, "/tarball/"):
			_, _ = w.Write(tarball)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dst := t.TempDir()
	src := marketplace.Source{
		Kind:     "npm",
		Package:  "mypkg",
		Version:  "latest",
		Registry: srv.URL,
	}
	fetcher := &marketplace.NPMFetcher{HTTPClient: srv.Client()}
	result, err := fetcher.Fetch(src, dst)
	if err != nil {
		t.Fatalf("NPM Fetch latest: %v", err)
	}
	if result.Version != "2.0.0" {
		t.Errorf("resolved version = %q, want 2.0.0", result.Version)
	}
}

// makeNPMTarballWithEntry builds an in-memory npm-style .tgz with one
// arbitrary entry name, bypassing the conventional "package/" prefix.
// Used to construct tarballs with traversal entries.
func makeNPMTarballWithEntry(t *testing.T, entryName, content string) []byte {
	t.Helper()
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "pkg.tgz")
	f, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	data := []byte(content)
	hdr := &tar.Header{Name: entryName, Size: int64(len(data)), Mode: 0o644, ModTime: time.Now()}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestNPMFetcher_TraversalEntryIsHardError replaces the previous
// silent-skip behavior: a tarball containing "package/../escape.txt"
// (which strips to "../escape.txt") must error out instead of being
// dropped. Silent skipping hid both attacks and corrupted tarballs.
func TestNPMFetcher_TraversalEntryIsHardError(t *testing.T) {
	// "package/../etc/passwd" → after the "package/" strip becomes
	// "../etc/passwd", which Clean+Join would resolve outside destDir.
	tarball := makeNPMTarballWithEntry(t, "package/../etc/passwd", "pwned")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/badpkg":
			meta := map[string]any{
				"versions": map[string]any{
					"1.0.0": map[string]any{
						"version": "1.0.0",
						"dist":    map[string]any{"tarball": "http://" + r.Host + "/tarball/1.0.0"},
					},
				},
				"dist-tags": map[string]any{"latest": "1.0.0"},
			}
			_ = json.NewEncoder(w).Encode(meta)
		case strings.HasPrefix(r.URL.Path, "/tarball/"):
			_, _ = w.Write(tarball)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dst := t.TempDir()
	src := marketplace.Source{Kind: "npm", Package: "badpkg", Version: "1.0.0", Registry: srv.URL}
	fetcher := &marketplace.NPMFetcher{HTTPClient: srv.Client()}
	_, err := fetcher.Fetch(src, dst)
	if err == nil {
		t.Fatal("expected error for traversal tarball entry")
	}
	if !strings.Contains(err.Error(), "escapes destination") {
		t.Fatalf("error %q did not mention escape", err.Error())
	}
}
