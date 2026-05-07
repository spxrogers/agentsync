package marketplace

import "fmt"

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
	switch src.Kind {
	case "github", "url", "git-subdir":
		return &GitFetcher{}
	case "npm":
		return &NPMFetcher{}
	}
	return &errFetcher{err: fmt.Errorf("unknown source kind %q", src.Kind)}
}
