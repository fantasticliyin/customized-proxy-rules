// SPDX-License-Identifier: GPL-3.0-only
package rulescsv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalSite(t *testing.T) {
	rule, err := CanonicalSite("DOMAIN-SUFFIX", " Example.COM. ", "@B @a @b")
	if err != nil {
		t.Fatal(err)
	}
	if rule.Value != "example.com" || len(rule.Attrs) != 2 || rule.Attrs[0] != "a" || rule.Attrs[1] != "b" {
		t.Fatalf("unexpected canonical rule: %#v", rule)
	}
	if _, err := CanonicalSite("DOMAIN-REGEX", "[", ""); err == nil {
		t.Fatal("invalid regex accepted")
	}
	if _, err := CanonicalSite("DOMAIN", "example.com", "name"); err == nil {
		t.Fatal("invalid attr accepted")
	}
}

func TestCanonicalIP(t *testing.T) {
	tests := map[string]string{"192.0.2.7/24": "192.0.2.0/24", "2001:db8::1/32": "2001:db8::/32", "192.0.2.1": "192.0.2.1/32"}
	for input, want := range tests {
		got, err := CanonicalIP(input)
		if err != nil {
			t.Fatal(err)
		}
		if got.Prefix.String() != want {
			t.Errorf("%s: got %s want %s", input, got.Prefix, want)
		}
	}
}

func TestValidateCategory(t *testing.T) {
	for _, category := range []string{"custom", "category-ai-!cn", "category_ip"} {
		if err := ValidateCategory(category); err != nil {
			t.Errorf("ValidateCategory(%q): %v", category, err)
		}
	}
	for _, category := range []string{"Category", "category@ads", "../category", "category.ai"} {
		if err := ValidateCategory(category); err == nil {
			t.Errorf("ValidateCategory(%q) accepted invalid category", category)
		}
	}
}

func TestLoadTreeQuotedAndConflict(t *testing.T) {
	root := ruleTree(t)
	write(t, filepath.Join(root, "geosite", "new", "custom.csv"), "op,type,value,attrs,note\n+,DOMAIN,Example.COM.,@ads,\"quoted, note\"\n")
	records, err := LoadTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Site.Value != "example.com" {
		t.Fatalf("unexpected records %#v", records)
	}
	write(t, filepath.Join(root, "geosite", "patch", "test.csv"), "op,type,value,attrs,note\n+,DOMAIN,example.org,@ads,a\n-,DOMAIN,example.org,,b\n")
	if _, err := LoadTree(root); err == nil {
		t.Fatal("wildcard conflict accepted")
	}
}

func TestLoadTreeExactHeaders(t *testing.T) {
	root := ruleTree(t)
	write(t, filepath.Join(root, "geoip", "new", "custom.csv"), "op,type,value,attrs,note\n+,IP-CIDR,192.0.2.1,,x\n")
	if _, err := LoadTree(root); err == nil {
		t.Fatal("wrong geoip header accepted")
	}
}

func ruleTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"geosite/new", "geosite/patch", "geoip/new", "geoip/patch"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func FuzzCanonicalIP(f *testing.F) {
	for _, seed := range []string{"192.0.2.1/24", "2001:db8::1", "bad"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		rule, err := CanonicalIP(input)
		if err != nil {
			return
		}
		again, err := CanonicalIP(rule.Prefix.String())
		if err != nil || again.Prefix != rule.Prefix {
			t.Fatalf("not idempotent: %q", input)
		}
	})
}
