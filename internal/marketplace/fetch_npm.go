package marketplace

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const defaultNPMRegistry = "https://registry.npmjs.org"

// MaxTarballBytes caps the total decompressed bytes from a single npm
// tarball. A 10KB zip-bomb expands to many GB; without a ceiling the
// extraction fills the user's disk and OOMs the process. 512 MiB is
// well above any legitimate plugin size and small enough to fail
// quickly on a malicious upload. Override via AGENTSYNC_MAX_TARBALL_MB.
const MaxTarballBytes = 512 * 1024 * 1024

// MaxMetadataBytes caps the npm registry metadata JSON response. A
// 10 GB malicious response would otherwise be buffered by json.Decoder.
const MaxMetadataBytes = 16 * 1024 * 1024

// NPMFetcher fetches npm packages by downloading the tarball directly from the
// registry HTTP API — no `npm` CLI required at runtime.
type NPMFetcher struct {
	// HTTPClient overrides the default http.DefaultClient. Used in tests to
	// inject a fake httptest.Server.
	HTTPClient *http.Client
}

type npmMetadata struct {
	Versions map[string]npmVersionMeta `json:"versions"`
	DistTags map[string]string         `json:"dist-tags"`
}

type npmVersionMeta struct {
	Version string  `json:"version"`
	Dist    npmDist `json:"dist"`
}

type npmDist struct {
	Tarball string `json:"tarball"`
}

// Fetch downloads the tarball for src.Package at src.Version into into.
// If src.Version is empty or "latest", the latest dist-tag is used.
func (f *NPMFetcher) Fetch(src Source, into string) (FetchResult, error) {
	if src.Package == "" {
		return FetchResult{}, fmt.Errorf("npm fetcher: package name is required")
	}

	registry := src.Registry
	if registry == "" {
		registry = defaultNPMRegistry
	}
	registry = strings.TrimRight(registry, "/")

	client := f.httpClient()

	// Fetch package metadata.
	metaURL := registry + "/" + src.Package
	meta, err := f.fetchMeta(client, metaURL)
	if err != nil {
		return FetchResult{}, fmt.Errorf("npm fetcher: fetch metadata for %s: %w", src.Package, err)
	}

	// Resolve version.
	version := src.Version
	if version == "" || version == "latest" {
		if tag, ok := meta.DistTags["latest"]; ok {
			version = tag
		}
	}
	if version == "" {
		return FetchResult{}, fmt.Errorf("npm fetcher: cannot determine version for %s", src.Package)
	}

	vMeta, ok := meta.Versions[version]
	if !ok {
		return FetchResult{}, fmt.Errorf("npm fetcher: version %s not found for %s", version, src.Package)
	}

	tarballURL := vMeta.Dist.Tarball
	if tarballURL == "" {
		return FetchResult{}, fmt.Errorf("npm fetcher: no tarball URL for %s@%s", src.Package, version)
	}
	// The tarball URL comes from registry metadata — attacker-influenced even
	// when the registry itself is https. Reject a plain-http (remote) tarball
	// before downloading so a MITM (or a hostile registry) cannot serve plugin
	// content over an unauthenticated transport.
	if err := checkURLScheme(tarballURL); err != nil {
		return FetchResult{}, fmt.Errorf("npm fetcher: tarball %w", err)
	}

	if err := os.MkdirAll(into, 0o755); err != nil {
		return FetchResult{}, fmt.Errorf("npm fetcher: mkdir %s: %w", into, err)
	}

	// Download and extract tarball.
	if err := f.downloadAndExtract(client, tarballURL, into); err != nil {
		return FetchResult{}, fmt.Errorf("npm fetcher: extract %s: %w", tarballURL, err)
	}

	return FetchResult{Version: version}, nil
}

func (f *NPMFetcher) httpClient() *http.Client {
	if f.HTTPClient != nil {
		return f.HTTPClient
	}
	return http.DefaultClient
}

func (f *NPMFetcher) fetchMeta(client *http.Client, url string) (*npmMetadata, error) {
	resp, err := client.Get(url) //nolint:noctx
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	// Cap the metadata body so a malicious or accidentally huge
	// response can't exhaust memory inside json.Decoder.
	limited := io.LimitReader(resp.Body, MaxMetadataBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	if int64(len(raw)) > MaxMetadataBytes {
		return nil, fmt.Errorf("npm metadata for %s exceeds %d byte cap (likely a malformed or hostile response)", url, MaxMetadataBytes)
	}
	var meta npmMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}
	return &meta, nil
}

// maxTarballBytes returns the active per-tarball cap. AGENTSYNC_MAX_TARBALL_MB
// overrides the default (set, for example, by users with legitimate
// massive plugins). Values <= 0 disable the cap.
func maxTarballBytes() int64 {
	v := os.Getenv("AGENTSYNC_MAX_TARBALL_MB")
	if v == "" {
		return MaxTarballBytes
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return MaxTarballBytes
	}
	if n <= 0 {
		return 0 // user opt-out
	}
	return n * 1024 * 1024
}

// downloadAndExtract downloads the gzipped tarball from url and extracts it
// into destDir, stripping the leading "package/" directory component that npm
// tarballs conventionally include.
//
// A per-tarball decompressed-bytes cap (MaxTarballBytes, overridable via
// AGENTSYNC_MAX_TARBALL_MB) protects against gzip-bombs and accidentally
// huge releases. The cap is enforced on the gzip stream (post-decompress
// bytes), not the wire bytes, so a 10 KB malicious .tgz that expands to
// many GB is stopped cold instead of filling the user's disk.
func (f *NPMFetcher) downloadAndExtract(client *http.Client, url, destDir string) error {
	resp, err := client.Get(url) //nolint:noctx
	if err != nil {
		return fmt.Errorf("GET tarball: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET tarball HTTP %d", resp.StatusCode)
	}

	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = gr.Close() }()

	cap := maxTarballBytes()
	// Wrap the gzip reader so we count post-decompress bytes against
	// the cap. A nil-cap (user opt-out) means we pass the gzip reader
	// straight through.
	var src io.Reader = gr
	if cap > 0 {
		src = &cappedReader{r: gr, remaining: cap, url: url}
	}

	tr := tar.NewReader(src)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}

		// Strip leading "package/" prefix (npm convention).
		name := hdr.Name
		if idx := strings.Index(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
		if name == "" {
			continue
		}

		// Security: prevent path traversal. Previously this silently
		// `continue`d on bad entries, which hid both attacks ("../etc/passwd")
		// and corrupted tarballs (everything after a stray ".." was skipped
		// and the plugin cache ended up half-populated with no diagnostic).
		// Hard-fail instead so the user knows the tarball is hostile or
		// broken.
		destPath := filepath.Join(destDir, filepath.Clean(name))
		if !strings.HasPrefix(destPath, destDir+string(os.PathSeparator)) && destPath != destDir {
			return fmt.Errorf("npm fetcher: tarball entry %q escapes destination %q (refusing to extract)", hdr.Name, destDir)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o755)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink, tar.TypeLink:
			// Explicit reject: even with destPath bounded, a symlink
			// could later be written through (TOCTOU) or surprise the
			// adapter expecting regular files. Plugin tarballs don't
			// need links — fail loud rather than silently drop.
			return fmt.Errorf("npm fetcher: tarball entry %q is a symlink/hardlink (refusing — plugin tarballs must be regular files)", hdr.Name)
		}
	}
	return nil
}

// cappedReader wraps an io.Reader and fails the Nth byte that would push
// total read past `remaining`. We don't use io.LimitReader because that
// silently returns io.EOF on overflow; we want a loud error so the user
// learns about the suspicious tarball rather than getting a half-extracted
// package cache.
type cappedReader struct {
	r         io.Reader
	remaining int64
	url       string
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.remaining < 0 {
		return 0, c.capErr()
	}
	// Allow reading up to remaining+1 bytes. A payload of exactly `cap`
	// bytes drains remaining to 0 without error (the tar reader's trailing
	// probe read then sees EOF), while the (cap+1)th byte drives remaining
	// negative and trips the cap. The previous `remaining <= 0` guard
	// rejected a tarball whose decompressed size was *exactly* the cap,
	// because tar issues one more Read after the last data byte.
	allow := c.remaining + 1
	if int64(len(p)) > allow {
		p = p[:allow]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	if c.remaining < 0 {
		return n, c.capErr()
	}
	return n, err
}

func (c *cappedReader) capErr() error {
	return fmt.Errorf("npm fetcher: decompressed tarball from %s exceeds cap (set AGENTSYNC_MAX_TARBALL_MB to override)", c.url)
}
