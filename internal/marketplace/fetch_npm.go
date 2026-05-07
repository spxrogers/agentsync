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
	"strings"
)

const defaultNPMRegistry = "https://registry.npmjs.org"

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
	var meta npmMetadata
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}
	return &meta, nil
}

// downloadAndExtract downloads the gzipped tarball from url and extracts it
// into destDir, stripping the leading "package/" directory component that npm
// tarballs conventionally include.
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
	defer gr.Close()

	tr := tar.NewReader(gr)
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

		// Security: prevent path traversal.
		destPath := filepath.Join(destDir, filepath.Clean(name))
		if !strings.HasPrefix(destPath, destDir+string(os.PathSeparator)) && destPath != destDir {
			continue
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
		}
	}
	return nil
}
