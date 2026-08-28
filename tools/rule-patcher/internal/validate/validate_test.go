// SPDX-License-Identifier: GPL-3.0-only
package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/artifact"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/geodat"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/upstream"
	v2ray "github.com/metacubex/geo/encoding/v2raygeo"
)

func TestDistRejectsDATAndSRSIndexMismatch(t *testing.T) {
	dist := fixture(t)
	if _, err := Dist(dist, geodat.DefaultMaxBytes); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
	if err := os.Rename(filepath.Join(dist, "geosite", "test.srs"), filepath.Join(dist, "geosite", "other.srs")); err != nil {
		t.Fatal(err)
	}
	if err := artifact.Finalize(dist, manifest()); err != nil {
		t.Fatal(err)
	}
	if _, err := Dist(dist, geodat.DefaultMaxBytes); err == nil || !strings.Contains(err.Error(), "DAT/SRS index mismatch") {
		t.Fatalf("index mismatch accepted: %v", err)
	}
}

func TestSafeRelative(t *testing.T) {
	for _, name := range []string{"geosite/test.srs", "manifest.json"} {
		if !safeRelative(name) {
			t.Fatalf("safe path rejected: %q", name)
		}
	}
	for _, name := range []string{"", ".", "../x", "a/../x", "/tmp/x", `a\b`} {
		if safeRelative(name) {
			t.Fatalf("unsafe path accepted: %q", name)
		}
	}
}

func fixture(t *testing.T) string {
	t.Helper()
	dist := t.TempDir()
	for _, dataset := range []string{"geosite", "geoip"} {
		if err := os.Mkdir(filepath.Join(dist, dataset), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dist, dataset, "test.srs"), []byte(dataset), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := geodat.WriteSite(filepath.Join(dist, "geosite.dat"), &v2ray.GeoSiteList{Entry: []*v2ray.GeoSite{{CountryCode: "test", Domain: []*v2ray.Domain{{Type: v2ray.Domain_Full, Value: "example.com"}}}}}, geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := geodat.WriteIP(filepath.Join(dist, "geoip.dat"), &v2ray.GeoIPList{Entry: []*v2ray.GeoIP{{CountryCode: "test", Cidr: []*v2ray.CIDR{{Ip: []byte{192, 0, 2, 0}, Prefix: 24}}}}}, geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := artifact.Finalize(dist, manifest()); err != nil {
		t.Fatal(err)
	}
	return dist
}

func manifest() artifact.Manifest {
	return artifact.Manifest{
		Version:           "local-c123456789abc",
		GeneratedAt:       time.Unix(1234, 0).UTC(),
		CustomCommit:      "123456789abcdef",
		ReleaseInputsHash: strings.Repeat("a", 64),
		Upstream:          upstream.Provenance{Repository: "MetaCubeX/meta-rules-dat", TagName: "local", PublishedAt: time.Unix(0, 0).UTC()},
		Tools: artifact.ToolVersions{
			SingBox: "v1", Mihomo: "v1", ConverterModule: "github.com/metacubex/meta-rules-converter", ConverterVersion: "v0.0.0", ConverterCommit: strings.Repeat("c", 40),
		},
		Categories: artifact.CategoryCount{GeoSite: 1, GeoIP: 1},
	}
}
