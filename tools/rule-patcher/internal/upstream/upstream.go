// SPDX-License-Identifier: GPL-3.0-only
package upstream

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxAPIResponse = 4 << 20

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type Asset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
	BrowserDownloadURL string `json:"browser_download_url"`
}
type Release struct {
	ID          int64     `json:"id"`
	TagName     string    `json:"tag_name"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}
type Provenance struct {
	Repository      string        `json:"repository"`
	ReleaseID       int64         `json:"release_id"`
	TagName         string        `json:"tag_name"`
	PublishedAt     time.Time     `json:"published_at"`
	GeoSiteSHA256   string        `json:"geosite_sha256"`
	GeoIPSHA256     string        `json:"geoip_sha256"`
	GeoSiteAsset    AssetEvidence `json:"geosite_asset"`
	GeoIPAsset      AssetEvidence `json:"geoip_asset"`
	GeoSiteChecksum AssetEvidence `json:"geosite_checksum_asset"`
	GeoIPChecksum   AssetEvidence `json:"geoip_checksum_asset"`
}

type AssetEvidence struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

func Evidence(asset Asset) AssetEvidence {
	return AssetEvidence{ID: asset.ID, Name: asset.Name, Size: asset.Size, Digest: strings.ToLower(asset.Digest)}
}

type Client struct {
	HTTP    *http.Client
	APIBase string
	Token   string
}

func NewClient(token string) *Client {
	return &Client{HTTP: &http.Client{Timeout: 60 * time.Second}, APIBase: "https://api.github.com", Token: token}
}

func (c *Client) Resolve(ctx context.Context, repository, releaseID string) (Release, error) {
	if !repositoryPattern.MatchString(repository) || strings.Contains(repository, "..") {
		return Release{}, fmt.Errorf("invalid repository")
	}
	endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(c.APIBase, "/"), repository)
	if releaseID != "" {
		id, err := strconv.ParseInt(releaseID, 10, 64)
		if err != nil || id <= 0 {
			return Release{}, fmt.Errorf("upstream release ID must be a positive integer")
		}
		endpoint = fmt.Sprintf("%s/repos/%s/releases/%d", strings.TrimRight(c.APIBase, "/"), repository, id)
	}
	var release Release
	if err := c.getJSON(ctx, endpoint, &release); err != nil {
		return release, err
	}
	if release.ID <= 0 || release.PublishedAt.IsZero() {
		return release, fmt.Errorf("GitHub response is missing immutable release identity")
	}
	if releaseID != "" && strconv.FormatInt(release.ID, 10) != releaseID {
		return release, fmt.Errorf("GitHub returned release %d, requested %s", release.ID, releaseID)
	}
	return release, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("GitHub API request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("GitHub API returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse+1))
	if err != nil {
		return fmt.Errorf("read GitHub response: %w", err)
	}
	if len(data) > maxAPIResponse {
		return fmt.Errorf("GitHub API response exceeds %d bytes", maxAPIResponse)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode GitHub response: trailing data")
	}
	return nil
}

func SelectAssets(release Release, names []string) (map[string]Asset, error) {
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	out := map[string]Asset{}
	for _, asset := range release.Assets {
		if wanted[asset.Name] {
			if _, exists := out[asset.Name]; exists {
				return nil, fmt.Errorf("release contains duplicate asset %q", asset.Name)
			}
			if asset.ID <= 0 || asset.Size <= 0 || asset.BrowserDownloadURL == "" {
				return nil, fmt.Errorf("asset %q is missing ID, size, or URL", asset.Name)
			}
			out[asset.Name] = asset
		}
	}
	for name := range wanted {
		if _, ok := out[name]; !ok {
			return nil, fmt.Errorf("release is missing asset %q", name)
		}
	}
	return out, nil
}

func (c *Client) DownloadVerified(ctx context.Context, dataAsset, checksumAsset Asset, destination string, maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		return "", fmt.Errorf("download size limit must be positive")
	}
	checksum, err := c.downloadChecksum(ctx, checksumAsset, dataAsset.Name)
	if err != nil {
		return "", err
	}
	apiDigest, err := apiDigest(dataAsset)
	if err != nil {
		return "", fmt.Errorf("asset %s has invalid API digest %q", dataAsset.Name, dataAsset.Digest)
	}
	if checksum != apiDigest {
		return "", fmt.Errorf("asset %s API digest and checksum asset disagree", dataAsset.Name)
	}
	if dataAsset.Size > maxBytes {
		return "", fmt.Errorf("asset %s size %d exceeds limit %d", dataAsset.Name, dataAsset.Size, maxBytes)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", err
	}
	tmp := destination + ".tmp"
	digest, size, err := c.download(ctx, dataAsset.BrowserDownloadURL, tmp, maxBytes)
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if size != dataAsset.Size {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("asset %s size mismatch: API %d, downloaded %d", dataAsset.Name, dataAsset.Size, size)
	}
	if digest != apiDigest {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("asset %s digest mismatch", dataAsset.Name)
	}
	if err := os.Rename(tmp, destination); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return digest, nil
}

func (c *Client) downloadChecksum(ctx context.Context, asset Asset, dataName string) (string, error) {
	if asset.ID <= 0 || asset.Size <= 0 || asset.Size > 64<<10 {
		return "", fmt.Errorf("checksum asset %s has invalid identity or size", asset.Name)
	}
	wantDigest, err := apiDigest(asset)
	if err != nil {
		return "", fmt.Errorf("checksum asset %s has invalid API digest", asset.Name)
	}
	tmp, err := os.CreateTemp("", "rule-patcher-checksum-")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)
	gotDigest, gotSize, err := c.download(ctx, asset.BrowserDownloadURL, path, 64<<10)
	if err != nil {
		return "", err
	}
	if gotSize != asset.Size || gotDigest != wantDigest {
		return "", fmt.Errorf("checksum asset %s metadata mismatch", asset.Name)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return "", fmt.Errorf("checksum asset %s is empty", asset.Name)
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) != 2 || len(fields[0]) != 64 {
		return "", fmt.Errorf("checksum asset %s is malformed", asset.Name)
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return "", fmt.Errorf("checksum asset %s is malformed", asset.Name)
	}
	if strings.TrimPrefix(fields[1], "*") != dataName {
		return "", fmt.Errorf("checksum asset %s names %q, expected %q", asset.Name, fields[1], dataName)
	}
	if scanner.Scan() || scanner.Err() != nil {
		return "", fmt.Errorf("checksum asset %s must contain exactly one line", asset.Name)
	}
	return strings.ToLower(fields[0]), nil
}

func (c *Client) download(ctx context.Context, rawURL, destination string, maxBytes int64) (string, int64, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || !c.allowedScheme(parsed) {
		return "", 0, fmt.Errorf("invalid asset URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", 0, err
	}
	if c.Token != "" && parsed.Host == "api.github.com" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.do(req)
	if err != nil {
		return "", 0, fmt.Errorf("download asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.Request == nil || resp.Request.URL == nil || !c.allowedScheme(resp.Request.URL) {
		return "", 0, fmt.Errorf("asset download redirected to an insecure URL")
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("download asset returned %s", resp.Status)
	}
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxBytes+1))
	closeErr := f.Close()
	if copyErr != nil {
		return "", n, copyErr
	}
	if closeErr != nil {
		return "", n, closeErr
	}
	if n > maxBytes {
		return "", n, fmt.Errorf("download exceeds limit %d", maxBytes)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func apiDigest(asset Asset) (string, error) {
	digest := strings.TrimPrefix(strings.ToLower(asset.Digest), "sha256:")
	if len(digest) != 64 {
		return "", fmt.Errorf("invalid digest")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", err
	}
	return digest, nil
}

func (c *Client) allowedScheme(candidate *url.URL) bool {
	if candidate.Scheme == "https" {
		return true
	}
	base, err := url.Parse(c.APIBase)
	return err == nil && base.Scheme == "http" && candidate.Scheme == "http" && candidate.Host == base.Host
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := c.HTTP.Do(req.Clone(req.Context()))
		if err == nil && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return resp, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("HTTP returned %s", resp.Status)
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
		}
		if attempt == 2 {
			break
		}
		delay := time.Duration(200*(1<<attempt)) * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}
