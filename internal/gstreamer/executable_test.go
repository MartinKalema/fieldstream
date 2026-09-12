package gstreamer

import (
	"os"
	"path/filepath"
	"testing"
)

func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExecutableSelectionPrefersOverrideThenPrivateThenPATH(t *testing.T) {
	root := t.TempDir()
	pathDirectory := t.TempDir()
	pathExecutable := writeExecutable(t, filepath.Join(pathDirectory, "gst-launch-1.0"))
	t.Setenv("PATH", pathDirectory)
	resolved, err := ResolveExecutable(root, "")
	if err != nil || resolved != pathExecutable || !filepath.IsAbs(resolved) {
		t.Fatalf("PATH executable not resolved absolutely: %q %v", resolved, err)
	}
	privateExecutable := writeExecutable(t, filepath.Join(root, ".tools", privateRuntime, "gst-launch-1.0"))
	resolved, err = ResolveExecutable(root, "")
	if err != nil || resolved != privateExecutable {
		t.Fatalf("private executable did not win: %q %v", resolved, err)
	}
	resolved, err = ResolveExecutable(root, pathExecutable)
	if err != nil || resolved != pathExecutable {
		t.Fatalf("explicit override did not win: %q %v", resolved, err)
	}
	if _, err := ResolveExecutable(root, filepath.Join(root, "missing-override")); err == nil {
		t.Fatal("missing explicit override silently fell back")
	}
	t.Chdir(root)
	writeExecutable(t, filepath.Join(root, "relative-launcher"))
	resolved, err = ResolveExecutable(root, "./relative-launcher")
	if err != nil || resolved != filepath.Join(root, "relative-launcher") {
		t.Fatalf("relative override did not become absolute: %q %v", resolved, err)
	}
}

func TestBrokenPrivateRuntimeDoesNotFallBackToPATH(t *testing.T) {
	pathDirectory := t.TempDir()
	pathExecutable := writeExecutable(t, filepath.Join(pathDirectory, "gst-launch-1.0"))
	t.Setenv("PATH", pathDirectory)
	for _, mode := range []string{"missing-wrapper", "nonexecutable-wrapper", "dangling-wrapper", "dangling-directory"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			runtimeDirectory := filepath.Join(root, ".tools", privateRuntime)
			if err := os.MkdirAll(filepath.Dir(runtimeDirectory), 0700); err != nil {
				t.Fatal(err)
			}
			if mode == "dangling-directory" {
				if err := os.Symlink(filepath.Join(root, "missing"), runtimeDirectory); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(runtimeDirectory, 0700); err != nil {
					t.Fatal(err)
				}
				wrapper := filepath.Join(runtimeDirectory, "gst-launch-1.0")
				if mode == "dangling-wrapper" {
					if err := os.Symlink(filepath.Join(root, "missing"), wrapper); err != nil {
						t.Fatal(err)
					}
				} else if mode == "nonexecutable-wrapper" {
					if err := os.WriteFile(wrapper, []byte("not executable"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := ResolveExecutable(root, ""); err == nil {
				t.Fatal("broken private runtime silently fell back to PATH")
			}
			if got, err := ResolveExecutable(root, pathExecutable); err != nil || got != pathExecutable {
				t.Fatalf("explicit override should still be usable: %q %v", got, err)
			}
		})
	}
}
