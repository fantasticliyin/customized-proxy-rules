// SPDX-License-Identifier: GPL-3.0-only
package geodat

import (
	v2ray "github.com/metacubex/geo/encoding/v2raygeo"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReloadDeterministicAndLimit(t *testing.T) {
	msg := &v2ray.GeoSiteList{Entry: []*v2ray.GeoSite{{CountryCode: "x", Domain: []*v2ray.Domain{{Type: v2ray.Domain_Full, Value: "example.com"}}}}}
	one := filepath.Join(t.TempDir(), "one.dat")
	two := filepath.Join(t.TempDir(), "two.dat")
	if err := WriteSite(one, msg, DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := WriteSite(two, msg, DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(one)
	b, _ := os.ReadFile(two)
	if string(a) != string(b) {
		t.Fatal("deterministic marshal differed")
	}
	if _, err := LoadSite(one, 1); err == nil {
		t.Fatal("size limit ignored")
	}
}
