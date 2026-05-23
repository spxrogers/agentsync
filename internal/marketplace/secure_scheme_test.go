package marketplace

import (
	"strings"
	"testing"
)

func TestDispatch_RejectsHTTP(t *testing.T) {
	t.Setenv("AGENTSYNC_ALLOW_INSECURE_URLS", "")
	f := Dispatch(Source{Kind: "url", URL: "http://example.com/plugin.git"})
	_, err := f.Fetch(Source{Kind: "url", URL: "http://example.com/plugin.git"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "insecure scheme") {
		t.Fatalf("http URL should be rejected; got err=%v", err)
	}
}

func TestDispatch_RejectsGitProtocol(t *testing.T) {
	t.Setenv("AGENTSYNC_ALLOW_INSECURE_URLS", "")
	f := Dispatch(Source{Kind: "url", URL: "git://example.com/plugin.git"})
	_, err := f.Fetch(Source{Kind: "url", URL: "git://example.com/plugin.git"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "insecure scheme") {
		t.Fatalf("git:// URL should be rejected; got err=%v", err)
	}
}

func TestDispatch_AllowsHTTPS(t *testing.T) {
	t.Setenv("AGENTSYNC_ALLOW_INSECURE_URLS", "")
	src := Source{Kind: "url", URL: "https://example.com/plugin.git"}
	// We don't actually clone; just verify Dispatch returns the git
	// fetcher, not the errFetcher.
	f := Dispatch(src)
	if _, ok := f.(*GitFetcher); !ok {
		t.Fatalf("https URL should dispatch to GitFetcher; got %T", f)
	}
}

func TestDispatch_AllowsGitHubShorthand(t *testing.T) {
	src := Source{Kind: "github", Repo: "owner/repo"}
	f := Dispatch(src)
	if _, ok := f.(*GitFetcher); !ok {
		t.Fatalf("github shorthand should dispatch to GitFetcher; got %T", f)
	}
}

func TestDispatch_OverrideAllowsHTTP(t *testing.T) {
	t.Setenv("AGENTSYNC_ALLOW_INSECURE_URLS", "1")
	src := Source{Kind: "url", URL: "http://example.com/plugin.git"}
	f := Dispatch(src)
	if _, ok := f.(*GitFetcher); !ok {
		t.Fatalf("env override should allow http; got %T", f)
	}
}

// TestDispatch_RejectsInsecureNPMRegistry is the regression for the npm blind
// spot: enforceSecureScheme originally inspected only URL and Repo, so an npm
// source pointing at a plain-http registry sailed through and a MITM could
// swap the metadata (and thus the resolved tarball URL). The registry must be
// scheme-checked like any other remote.
func TestDispatch_RejectsInsecureNPMRegistry(t *testing.T) {
	t.Setenv("AGENTSYNC_ALLOW_INSECURE_URLS", "")
	src := Source{Kind: "npm", Package: "x", Registry: "http://registry.evil.example.com"}
	_, err := Dispatch(src).Fetch(src, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "insecure scheme") {
		t.Fatalf("plain-http npm registry should be rejected; got err=%v", err)
	}
}

// TestDispatch_AllowsLoopbackHTTP documents the deliberate carve-out: http to
// a loopback host has no MITM surface (the request never leaves the machine),
// so local mirrors — and the httptest fixtures the npm fetcher tests depend on
// — are allowed even though plain http to a remote host is rejected.
func TestDispatch_AllowsLoopbackHTTP(t *testing.T) {
	t.Setenv("AGENTSYNC_ALLOW_INSECURE_URLS", "")
	for _, raw := range []string{
		"http://127.0.0.1:8080/p.git",
		"http://localhost/p.git",
		"http://[::1]:9/p.git",
	} {
		if _, ok := Dispatch(Source{Kind: "url", URL: raw}).(*GitFetcher); !ok {
			t.Errorf("loopback http %q should dispatch to GitFetcher (no MITM surface)", raw)
		}
	}
}
