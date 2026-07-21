package addrgen

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriverRequiresVersionedJSON(t *testing.T) {
	for _, version := range []string{"", "v2"} {
		t.Run("version_"+version, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "addrgen")
			script := "#!/bin/sh\nprintf '%s\\n' '{\"version\":\"" + version + "\",\"status\":\"ok\",\"address\":\"jregtest1one\",\"start\":0,\"count\":1,\"addresses\":[\"jregtest1one\"]}'\n"
			if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			deriver := Deriver{Path: path}
			if _, err := deriver.Derive(context.Background(), "jviewregtest1test", 0); err == nil {
				t.Fatal("single derivation accepted an unsupported JSON version")
			}
			if _, err := deriver.DeriveBatch(context.Background(), "jviewregtest1test", 0, 1); err == nil {
				t.Fatal("batch derivation accepted an unsupported JSON version")
			}
		})
	}
}

func TestDeriverAcceptsV1JSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "addrgen")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"version\":\"v1\",\"status\":\"ok\",\"address\":\"jregtest1one\",\"start\":0,\"count\":1,\"addresses\":[\"jregtest1one\"]}'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	deriver := Deriver{Path: path}
	address, err := deriver.Derive(context.Background(), "jviewregtest1secret", 0)
	if err != nil || address != "jregtest1one" {
		t.Fatalf("single address=%q err=%v", address, err)
	}
	addresses, err := deriver.DeriveBatch(context.Background(), "jviewregtest1secret", 0, 1)
	if err != nil || len(addresses) != 1 || strings.TrimSpace(addresses[0]) != "jregtest1one" {
		t.Fatalf("batch addresses=%v err=%v", addresses, err)
	}
}
