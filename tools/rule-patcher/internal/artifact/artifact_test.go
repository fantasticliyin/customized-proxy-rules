// SPDX-License-Identifier: GPL-3.0-only
package artifact

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/geodat"
	v2ray "github.com/metacubex/geo/encoding/v2raygeo"
)

func TestFinalizeDeterministic(t *testing.T) {
	one := fixtureDist(t)
	two := fixtureDist(t)
	manifest := Manifest{Version: "u1-c123456789abc", GeneratedAt: time.Unix(1234, 0).UTC(), CustomCommit: "123456789abcdef", ReleaseInputsHash: "hash", Categories: CategoryCount{GeoSite: 1, GeoIP: 1}}
	if err := Finalize(one, manifest); err != nil {
		t.Fatal(err)
	}
	if err := Finalize(two, manifest); err != nil {
		t.Fatal(err)
	}
	a, err := HashTree(one)
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashTree(two)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("artifact trees differ: %s != %s", a, b)
	}
}
func fixtureDist(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"geosite", "geoip"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "test.srs"), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := geodat.WriteSite(filepath.Join(dir, "geosite.dat"), &v2ray.GeoSiteList{Entry: []*v2ray.GeoSite{{CountryCode: "test", Domain: []*v2ray.Domain{{Type: v2ray.Domain_Full, Value: "example.com"}}}}}, geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := geodat.WriteIP(filepath.Join(dir, "geoip.dat"), &v2ray.GeoIPList{Entry: []*v2ray.GeoIP{{CountryCode: "test", Cidr: []*v2ray.CIDR{{Ip: []byte{192, 0, 2, 0}, Prefix: 24}}}}}, geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	return dir
}
