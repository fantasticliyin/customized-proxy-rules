// SPDX-License-Identifier: GPL-3.0-only
package srs

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGenerateValidatesIndexAndCompiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	converter := filepath.Join(dir, "converter")
	sing := filepath.Join(dir, "sing-box")
	writeExecutable(t, converter, "#!/bin/sh\nout=''\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = '-o' ]; then out=$2; shift 2; else shift; fi\ndone\nmkdir -p \"$out\"\nprintf '{\"version\":2,\"rules\":[{}]}\\n' > \"$out/test.json\"\nprintf old > \"$out/test.srs\"\n")
	writeExecutable(t, sing, "#!/bin/sh\ncp \"$5\" \"$4\"\n")
	out := filepath.Join(dir, "out")
	if err := Generate(context.Background(), Tools{Converter: converter, SingBox: sing, Workers: 2}, "geosite", filepath.Join(dir, "input.dat"), out, []string{"test"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(out, "test.srs"))
	if err != nil || info.Size() == 0 {
		t.Fatal("compiled SRS missing")
	}
	if err := Generate(context.Background(), Tools{Converter: converter, SingBox: sing, Workers: 1}, "geosite", filepath.Join(dir, "input.dat"), filepath.Join(dir, "bad"), []string{"other"}); err == nil {
		t.Fatal("index mismatch accepted")
	}
}

func TestSafeNameAllowsUpstreamNegatedAttribute(t *testing.T) {
	if !safeName("ipip@!cn") {
		t.Fatal("real upstream attribute view rejected")
	}
	for _, name := range []string{"../x", "a/b", "a\\b", ""} {
		if safeName(name) {
			t.Fatalf("unsafe name accepted: %q", name)
		}
	}
}
func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
