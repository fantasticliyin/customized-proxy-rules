// SPDX-License-Identifier: GPL-3.0-only
package toolchain

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/config"
)

type Paths struct {
	SingBox   string
	Mihomo    string
	Converter string
}

type cacheManifest struct {
	Lock         config.Toolchain `json:"lock"`
	SingBoxSHA   string           `json:"sing_box_sha256"`
	MihomoSHA    string           `json:"mihomo_sha256"`
	ConverterSHA string           `json:"converter_sha256"`
}

func Prepare(ctx context.Context, lock config.Toolchain, cacheRoot string) (Paths, error) {
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return Paths{}, err
	}
	encoded, _ := json.Marshal(lock)
	sum := sha256.Sum256(encoded)
	root := filepath.Join(cacheRoot, hex.EncodeToString(sum[:16]))
	paths := Paths{SingBox: filepath.Join(root, "sing-box"), Mihomo: filepath.Join(root, "mihomo"), Converter: filepath.Join(root, "meta-rules-converter")}
	if ready(paths, lock) {
		return paths, nil
	}
	stage, err := os.MkdirTemp(cacheRoot, ".toolchain-")
	if err != nil {
		return paths, err
	}
	defer os.RemoveAll(stage)
	client := &http.Client{Timeout: 5 * time.Minute}
	singArchive := filepath.Join(stage, "sing-box.tar.gz")
	if err := download(ctx, client, lock.SingBox.URL, lock.SingBox.SHA256, singArchive, 256<<20); err != nil {
		return paths, err
	}
	if err := extractExecutable(singArchive, "sing-box", filepath.Join(stage, "sing-box")); err != nil {
		return paths, err
	}
	mihomoArchive := filepath.Join(stage, "mihomo.gz")
	if err := download(ctx, client, lock.Mihomo.URL, lock.Mihomo.SHA256, mihomoArchive, 256<<20); err != nil {
		return paths, err
	}
	if err := gunzipExecutable(mihomoArchive, filepath.Join(stage, "mihomo")); err != nil {
		return paths, err
	}
	listCmd := exec.CommandContext(ctx, "go", "list", "-m", "-json", lock.Converter.Module+"@"+lock.Converter.Version)
	listOutput, err := listCmd.Output()
	if err != nil {
		return paths, fmt.Errorf("resolve converter provenance: %w", err)
	}
	var resolved struct {
		Path    string `json:"Path"`
		Version string `json:"Version"`
		Origin  struct {
			VCS  string `json:"VCS"`
			URL  string `json:"URL"`
			Hash string `json:"Hash"`
		} `json:"Origin"`
	}
	if len(listOutput) > 1<<20 || json.Unmarshal(listOutput, &resolved) != nil || resolved.Path != lock.Converter.Module || resolved.Version != lock.Converter.Version || resolved.Origin.VCS != "git" || resolved.Origin.URL != "https://github.com/metacubex/meta-rules-converter" || resolved.Origin.Hash != lock.Converter.Commit {
		return paths, fmt.Errorf("resolved converter module does not match locked full commit")
	}
	cmd := exec.CommandContext(ctx, "go", "install", lock.Converter.Module+"@"+lock.Converter.Version)
	cmd.Env = append(os.Environ(), "GOBIN="+stage)
	output, err := runCommand(cmd)
	if err != nil {
		return paths, fmt.Errorf("build converter %s: %w: %s", lock.Converter.Version, err, limit(output))
	}
	built := filepath.Join(stage, "meta-rules-converter")
	if _, err := os.Stat(built); os.IsNotExist(err) {
		fallback := filepath.Join(stage, "convert")
		if _, e := os.Stat(fallback); e == nil {
			built = fallback
		} else {
			return paths, fmt.Errorf("go install did not produce converter executable")
		}
	}
	if err := os.Chmod(built, 0o755); err != nil {
		return paths, err
	}
	if built != filepath.Join(stage, "meta-rules-converter") {
		if err := os.Rename(built, filepath.Join(stage, "meta-rules-converter")); err != nil {
			return paths, err
		}
	}
	if err := verifyConverter(filepath.Join(stage, "meta-rules-converter"), lock); err != nil {
		return paths, err
	}
	manifest := cacheManifest{Lock: lock}
	manifest.SingBoxSHA, err = hashFile(filepath.Join(stage, "sing-box"))
	if err != nil {
		return paths, err
	}
	manifest.MihomoSHA, err = hashFile(filepath.Join(stage, "mihomo"))
	if err != nil {
		return paths, err
	}
	manifest.ConverterSHA, err = hashFile(filepath.Join(stage, "meta-rules-converter"))
	if err != nil {
		return paths, err
	}
	marker, err := json.Marshal(manifest)
	if err != nil {
		return paths, err
	}
	if err := os.WriteFile(filepath.Join(stage, "provenance.json"), marker, 0o600); err != nil {
		return paths, err
	}
	if err := os.RemoveAll(root); err != nil {
		return paths, err
	}
	if err := os.Rename(stage, root); err != nil {
		if ready(paths, lock) {
			return paths, nil
		}
		return paths, err
	}
	return paths, nil
}

func ready(paths Paths, lock config.Toolchain) bool {
	for _, path := range []string{paths.SingBox, paths.Mihomo, paths.Converter} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			return false
		}
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(paths.SingBox), "provenance.json"))
	if err != nil {
		return false
	}
	var manifest cacheManifest
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.Lock != lock {
		return false
	}
	checks := []struct{ path, want string }{{paths.SingBox, manifest.SingBoxSHA}, {paths.Mihomo, manifest.MihomoSHA}, {paths.Converter, manifest.ConverterSHA}}
	for _, check := range checks {
		got, err := hashFile(check.path)
		if err != nil || got != check.want {
			return false
		}
	}
	return verifyConverter(paths.Converter, lock) == nil
}
func download(ctx context.Context, client *http.Client, rawURL, want, path string, max int64) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return fmt.Errorf("tool URL must use HTTPS")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := doRequest(ctx, client, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.Request == nil || resp.Request.URL == nil || resp.Request.URL.Scheme != "https" {
		return fmt.Errorf("tool download redirected to an insecure URL")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", parsed.Host, resp.Status)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, max+1))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > max {
		return fmt.Errorf("tool download exceeds limit")
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("tool checksum mismatch: got %s", got)
	}
	return nil
}

func doRequest(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := client.Do(req.Clone(ctx))
		if err == nil && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return resp, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("tool download returned %s", resp.Status)
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(200*(1<<attempt)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}
func extractExecutable(archive, name, target string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != name {
			continue
		}
		if found {
			return fmt.Errorf("archive contains multiple %s executables", name)
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(out, io.LimitReader(tr, (256<<20)+1))
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n > 256<<20 {
			return fmt.Errorf("archive executable exceeds limit")
		}
		found = true
	}
	if !found {
		return fmt.Errorf("archive does not contain %s", name)
	}
	return nil
}
func gunzipExecutable(archive, target string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, io.LimitReader(gz, (256<<20)+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > 256<<20 {
		return fmt.Errorf("decompressed executable exceeds limit")
	}
	return nil
}
func limit(data []byte) string {
	if len(data) > 4096 {
		data = data[:4096]
	}
	return strings.TrimSpace(string(data))
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyConverter(path string, lock config.Toolchain) error {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read converter build provenance: %w", err)
	}
	if info.Path != lock.Converter.Module || info.Main.Version != lock.Converter.Version {
		return fmt.Errorf("converter provenance mismatch: got %s@%s", info.Path, info.Main.Version)
	}
	return nil
}

type cappedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.max - w.buf.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = w.buf.Write(p[:remaining])
	}
	return len(p), nil
}

func runCommand(cmd *exec.Cmd) ([]byte, error) {
	output := &cappedBuffer{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	return output.buf.Bytes(), err
}
