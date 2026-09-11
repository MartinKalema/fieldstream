package lab

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMediaSelectionPreservesOfficialAndBrokenSelection(t *testing.T) {
	p := NewPaths(t.TempDir())
	official := filepath.Join(p.Tools, "mediamtx-v1.21.0", "mediamtx")
	if p.BinaryPath() != official {
		t.Fatal("an unconfigured workspace must use the pinned official release")
	}
	if err := os.MkdirAll(p.Tools, 0700); err != nil {
		t.Fatal(err)
	}
	selection := filepath.Join(p.Tools, "mediamtx-active")
	// Deliberately point at a missing correction. Falling back here would hide
	// a broken installation and restore the original latency defect unnoticed.
	if err := os.Symlink("mediamtx-v1.21.0-clockfix1", selection); err != nil {
		t.Fatal(err)
	}
	if p.BinaryPath() != filepath.Join(selection, "mediamtx") {
		t.Fatal("a broken explicit selection must not silently fall back")
	}
	if p.officialMediaMTXPath() != official {
		t.Fatal("selection changed the path managed by the official installer")
	}
	if err := os.Remove(selection); err != nil {
		t.Fatal(err)
	}
	if p.BinaryPath() != official {
		t.Fatal("removing the selection must restore the original release")
	}
}
