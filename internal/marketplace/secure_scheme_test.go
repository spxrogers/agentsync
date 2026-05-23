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
