package marketplace

import (
	"fmt"
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

// enforceSecureScheme rejects plain-text URL schemes (http://, git://) so a
// MITM cannot swap plugin content. Loopback file:// is allowed for test
// fixtures, and ssh:// is allowed since SSH provides its own integrity. Users
// who need a plain-http internal mirror can set AGENTSYNC_ALLOW_INSECURE_URLS=1.
func enforceSecureScheme(src Source) error {
	if os.Getenv("AGENTSYNC_ALLOW_INSECURE_URLS") == "1" {
		return nil
	}
	candidates := []string{src.URL, src.Repo}
	for _, raw := range candidates {
		if raw == "" {
			continue
		}
		// GitHub repos are written as "owner/repo" with no scheme; the
		// GitFetcher prepends https://. Skip those.
		if !strings.Contains(raw, "://") {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("parse plugin URL %q: %w", raw, err)
		}
		switch strings.ToLower(u.Scheme) {
		case "https", "ssh", "git+ssh", "file":
			// ok
		case "http", "git":
			return fmt.Errorf("plugin URL %q uses insecure scheme %q; "+
				"set AGENTSYNC_ALLOW_INSECURE_URLS=1 to override (internal mirrors only)",
				raw, u.Scheme)
		}
	}
	return nil
}
