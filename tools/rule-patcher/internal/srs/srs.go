// SPDX-License-Identifier: GPL-3.0-only
package srs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/geodat"
	v2ray "github.com/metacubex/geo/encoding/v2raygeo"
)

type Tools struct {
	Converter string
	SingBox   string
	Workers   int
}

func Generate(ctx context.Context, tools Tools, dataset, datPath, outputDir string, expected []string) error {
	if tools.Converter == "" || tools.SingBox == "" {
		return fmt.Errorf("converter and sing-box executable paths are required")
	}
	if tools.Workers < 1 {
		tools.Workers = 1
	}
	if tools.Workers > 16 {
		tools.Workers = 16
	}
	if dataset != "geosite" && dataset != "geoip" {
		return fmt.Errorf("unsupported converter dataset %q", dataset)
	}
	if _, err := os.Lstat(outputDir); err == nil {
		return fmt.Errorf("SRS output directory already exists: %s", outputDir)
	} else if !os.IsNotExist(err) {
		return err
	}
	temp, err := os.MkdirTemp(filepath.Dir(outputDir), ".srs-source-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	cmd := exec.CommandContext(ctx, tools.Converter, dataset, "-f", datPath, "-o", temp, "-t", "sing-box")
	if output, err := runCommand(cmd); err != nil {
		return fmt.Errorf("converter %s failed: %w: %s", dataset, err, limitText(output))
	}
	actual, err := sourceIndex(temp)
	if err != nil {
		return err
	}
	expected = append([]string(nil), expected...)
	sort.Strings(expected)
	if !equal(expected, actual) {
		return fmt.Errorf("%s converter index mismatch: expected %v, got %v", dataset, expected, actual)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	type job struct{ name string }
	jobs := make(chan job)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for i := 0; i < tools.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				source := filepath.Join(temp, item.name+".json")
				target := filepath.Join(outputDir, item.name+".srs")
				cmd := exec.CommandContext(ctx, tools.SingBox, "rule-set", "compile", "--output", target, source)
				if output, err := runCommand(cmd); err != nil {
					select {
					case errCh <- fmt.Errorf("compile %s/%s: %w: %s", dataset, item.name, err, limitText(output)):
					default:
					}
					return
				}
				info, err := os.Stat(target)
				if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
					select {
					case errCh <- fmt.Errorf("compile %s/%s produced no SRS", dataset, item.name):
					default:
					}
					return
				}
				verify := target + ".verify.json"
				cmd = exec.CommandContext(ctx, tools.SingBox, "rule-set", "decompile", "--output", verify, target)
				if output, err := runCommand(cmd); err != nil {
					select {
					case errCh <- fmt.Errorf("load %s/%s SRS: %w: %s", dataset, item.name, err, limitText(output)):
					default:
					}
					return
				}
				if info, err := os.Stat(verify); err != nil || info.Size() == 0 {
					select {
					case errCh <- fmt.Errorf("decompile %s/%s produced no JSON", dataset, item.name):
					default:
					}
					return
				}
				_ = os.Remove(verify)
			}
		}()
	}
	for _, name := range expected {
		select {
		case jobs <- job{name}:
		case err := <-errCh:
			close(jobs)
			wg.Wait()
			return err
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	return nil
}

func MihomoSmoke(ctx context.Context, mihomo, geositeDAT, geoipDAT, siteCategory, ipCategory string) error {
	if mihomo == "" || !safeName(siteCategory) || !safeName(ipCategory) {
		return fmt.Errorf("mihomo executable and safe representative categories are required")
	}
	dir, err := os.MkdirTemp("", "rule-patcher-mihomo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for source, name := range map[string]string{geositeDAT: "GeoSite.dat", geoipDAT: "GeoIP.dat"} {
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	config := fmt.Sprintf("geodata-mode: true\nmode: rule\nlog-level: error\nproxies: []\nproxy-groups: []\nrules:\n  - GEOSITE,%s,DIRECT\n  - GEOIP,%s,DIRECT\n  - MATCH,DIRECT\n", siteCategory, ipCategory)
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, mihomo, "-d", dir, "-f", configPath, "-t")
	if output, err := runCommand(cmd); err != nil {
		return fmt.Errorf("mihomo GeoData smoke test failed: %w: %s", err, limitText(output))
	}
	return nil
}

// MihomoMetaDBSmoke asks Mihomo to load the published MetaDB mode and resolve
// a representative GEOIP category from it.
func MihomoMetaDBSmoke(ctx context.Context, mihomo, metaDB, ipCategory string) error {
	if mihomo == "" || !safeName(ipCategory) {
		return fmt.Errorf("mihomo executable and a safe representative category are required")
	}
	dir, err := os.MkdirTemp("", "rule-patcher-mihomo-metadb-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	data, err := os.ReadFile(metaDB)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "geoip.metadb"), data, 0o644); err != nil {
		return err
	}
	config := fmt.Sprintf("geodata-mode: false\nmode: rule\nlog-level: error\nproxies: []\nproxy-groups: []\nrules:\n  - GEOIP,%s,DIRECT,no-resolve\n  - MATCH,DIRECT\n", ipCategory)
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, mihomo, "-d", dir, "-f", configPath, "-t")
	if output, err := runCommand(cmd); err != nil {
		return fmt.Errorf("mihomo MetaDB smoke test failed: %w: %s", err, limitText(output))
	}
	return nil
}

// ValidateCompiled asks the locked sing-box binary to decode every published
// rule-set. Index validation remains the caller's responsibility.
func ValidateCompiled(ctx context.Context, singBox, root string, relativePaths []string) error {
	if singBox == "" {
		return fmt.Errorf("sing-box executable path is required")
	}
	temp, err := os.MkdirTemp("", "rule-patcher-srs-validate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	for i, rel := range relativePaths {
		if filepath.IsAbs(rel) || strings.Contains(rel, "..") || strings.Contains(rel, "\\") {
			return fmt.Errorf("unsafe SRS validation path %q", rel)
		}
		output := filepath.Join(temp, fmt.Sprintf("%06d.json", i))
		cmd := exec.CommandContext(ctx, singBox, "rule-set", "decompile", "--output", output, filepath.Join(root, filepath.FromSlash(rel)))
		if data, err := runCommand(cmd); err != nil {
			return fmt.Errorf("sing-box rejected %s: %w: %s", rel, err, limitText(data))
		}
		info, err := os.Stat(output)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 64<<20 {
			return fmt.Errorf("sing-box produced invalid JSON while validating %s", rel)
		}
		data, err := os.ReadFile(output)
		if err != nil {
			return fmt.Errorf("read decompiled %s: %w", rel, err)
		}
		var doc struct {
			Version int               `json:"version"`
			Rules   []json.RawMessage `json:"rules"`
		}
		if len(data) > 64<<20 || json.Unmarshal(data, &doc) != nil || doc.Version <= 0 || doc.Rules == nil {
			return fmt.Errorf("sing-box produced invalid JSON while validating %s", rel)
		}
	}
	return nil
}

func ExpectedSite(msg *v2ray.GeoSiteList) ([]string, error) {
	seen := map[string]bool{}
	for _, category := range msg.Entry {
		code := strings.ToLower(category.CountryCode)
		if !safeName(code) || strings.Contains(code, "@") {
			return nil, fmt.Errorf("unsafe geosite category %q", code)
		}
		seen[code] = true
		for _, domain := range category.Domain {
			for _, attr := range domain.Attribute {
				if strings.Contains(attr.Key, "@") {
					return nil, fmt.Errorf("unsafe geosite attribute key %q", attr.Key)
				}
				view := code + "@" + strings.ToLower(attr.Key)
				if !safeName(view) {
					return nil, fmt.Errorf("unsafe geosite attribute view %q", view)
				}
				seen[view] = true
			}
		}
	}
	return sortedKeys(seen), nil
}
func ExpectedIP(msg *v2ray.GeoIPList) ([]string, error) {
	seen := map[string]bool{}
	for _, category := range msg.Entry {
		code := strings.ToLower(category.CountryCode)
		if !safeName(code) || strings.Contains(code, "@") {
			return nil, fmt.Errorf("unsafe geoip category %q", code)
		}
		seen[code] = true
	}
	return sortedKeys(seen), nil
}
func sourceIndex(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("converter created unexpected directory %q", entry.Name())
		}
		ext := filepath.Ext(entry.Name())
		base := strings.TrimSuffix(entry.Name(), ext)
		if !safeName(base) {
			return nil, fmt.Errorf("converter created unsafe path %q", entry.Name())
		}
		if ext == ".srs" {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return nil, fmt.Errorf("discard converter SRS %q: %w", entry.Name(), err)
			}
			continue
		}
		if ext != ".json" {
			return nil, fmt.Errorf("converter created unexpected file %q", entry.Name())
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 64<<20 {
			return nil, fmt.Errorf("invalid converter JSON file %q", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var doc struct {
			Version int               `json:"version"`
			Rules   []json.RawMessage `json:"rules"`
		}
		if err := json.Unmarshal(data, &doc); err != nil || doc.Version <= 0 || doc.Rules == nil {
			return nil, fmt.Errorf("invalid converter JSON %q", entry.Name())
		}
		names = append(names, base)
	}
	sort.Strings(names)
	return names, nil
}
func safeName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '@' || r == '!') {
			return false
		}
	}
	return true
}
func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func limitText(data []byte) string {
	const max = 4096
	if len(data) > max {
		data = data[:max]
	}
	return strings.TrimSpace(string(data))
}

var _ = geodat.DefaultMaxBytes

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
