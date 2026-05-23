package marketplace

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// Fetcher fetches one plugin source into a local directory.
type Fetcher interface {
	// Fetch resolves src into the directory at into, creating it if necessary.
	// Returns a FetchResult with available metadata (HeadSHA for git sources,
	// Version for npm).
	Fetch(src Source, into string) (FetchResult, error)
}

// FetchResult carries metadata about a completed fetch.
type FetchResult struct {
	HeadSHA string // for git sources
	Version string // for npm
}

// errFetcher is a Fetcher that always returns a fixed error.
type errFetcher struct{ err error }

func (e *errFetcher) Fetch(_ Source, _ string) (FetchResult, error) {
	return FetchResult{}, e.err
}

// Dispatch returns the appropriate Fetcher for the given Source.
func Dispatch(src Source) Fetcher {
	if src.Relative != "" {
		return &RelativeFetcher{}
	}
	if err := enforceSecureScheme(src); err != nil {
		return &errFetcher{err: err}
	}
	switch src.Kind {
	case "github", "url", "git-subdir":
		return &GitFetcher{}
	case "npm":
		return &NPMFetcher{}
	}
	return &errFetcher{err: fmt.Errorf("unknown source kind %q", src.Kind)}
}

// enforceSecureScheme rejects plain-text URL schemes (http://, git://) on
// every remote a fetch will contact — the git URL/repo AND the npm registry —
// so a MITM cannot swap plugin content or the metadata that resolves the
// tarball URL. The registry was previously unchecked, leaving npm sources
// exposed even with the env override unset.
func enforceSecureScheme(src Source) error {
	for _, raw := range []string{src.URL, src.Repo, src.Registry} {
		if err := checkURLScheme(raw); err != nil {
			return err
		}
	}
	return nil
}

// checkURLScheme rejects a single remote URL that uses a plain-text scheme.
// Empty strings and scheme-less shorthand ("owner/repo") are accepted — the
// caller resolves those to https. Loopback http/git is allowed because it has
// no MITM surface (the request never leaves the machine), which covers local
// mirrors and the httptest fixtures the fetcher tests rely on; file:// and
// ssh:// are allowed since they carry their own integrity. Users who need a
// remote plain-http internal mirror can set AGENTSYNC_ALLOW_INSECURE_URLS=1.
func checkURLScheme(raw string) error {
	if os.Getenv("AGENTSYNC_ALLOW_INSECURE_URLS") == "1" {
		return nil
	}
	// GitHub repos are written as "owner/repo" with no scheme; the GitFetcher
	// prepends https://. Skip those (and empty fields).
	if raw == "" || !strings.Contains(raw, "://") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse plugin URL %q: %w", raw, err)
	}
	// Only the plain-text git transports are rejected; https, ssh, git+ssh,
	// file, and any other scheme are allowed (matches the prior default).
	scheme := strings.ToLower(u.Scheme)
	if scheme == "http" || scheme == "git" {
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("plugin URL %q uses insecure scheme %q; "+
			"set AGENTSYNC_ALLOW_INSECURE_URLS=1 to override (internal mirrors only)",
			raw, u.Scheme)
	}
	return nil
}

// isLoopbackHost reports whether host (a URL hostname, no port) refers to the
// local machine: the literal "localhost" or any loopback IP (127.0.0.0/8, ::1).
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
