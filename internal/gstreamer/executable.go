package gstreamer

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const privateRuntime = "gstreamer-1.28.7"

// ResolveExecutable prefers an explicit override, then the project's private
// runtime, then PATH. A selected but broken private runtime is an error; it must
// not silently change which GStreamer installation the viewer uses.
func ResolveExecutable(root, override string) (string, error) {
	if override != "" {
		path, err := absoluteExecutable(override)
		if err != nil {
			return "", fmt.Errorf("GStreamer executable override is unavailable: %w", err)
		}
		return path, nil
	}
	runtimeDirectory, err := filepath.Abs(filepath.Join(root, ".tools", privateRuntime))
	if err != nil {
		return "", fmt.Errorf("GStreamer private runtime path is unavailable: %w", err)
	}
	_, err = os.Lstat(runtimeDirectory)
	if !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return "", fmt.Errorf("cannot inspect the private GStreamer runtime: %w", err)
		}
		path, err := absoluteExecutable(filepath.Join(runtimeDirectory, "gst-launch-1.0"))
		if err != nil {
			return "", fmt.Errorf("the selected private GStreamer runtime is incomplete or unusable: %w", err)
		}
		return path, nil
	}
	path, err := absoluteExecutable("gst-launch-1.0")
	if err != nil {
		return "", errors.New("gst-launch-1.0 was not found; install the private GStreamer runtime or provide an executable override")
	}
	return path, nil
}

func absoluteExecutable(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}
