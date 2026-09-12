package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateAllFixedScenes(t *testing.T) {
	ffmpeg, ffprobe := qualityTools(t)
	for _, id := range []string{"fast-motion", "fine-detail", "dim-noise"} {
		t.Run(id, func(t *testing.T) {
			dir := t.TempDir()
			scene, err := generateScene(context.Background(), dir, ffmpeg, id)
			if err != nil {
				t.Fatal(err)
			}
			if scene.ID != id || scene.Title == "" || scene.Note == "" || scene.Filter == "" || scene.SourceEncoding == "" {
				t.Fatal("scene metadata is incomplete")
			}
			file := filepath.Join(dir, "original.mp4")
			media, err := inspectDecodedMedia(context.Background(), dir, file, ffprobe)
			if err != nil || media.Width != 1280 || media.Height != 720 || media.Frames != 150 || media.FPS != 30 || media.Duration != 5 || media.StartTime != 0 || media.Bytes > maximumInputBytes {
				t.Fatalf("generated scene is not the fixed complete 720p30 source: %+v %v", media, err)
			}
			info, err := os.Stat(file)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("synthetic footage is not private")
			}
		})
	}
}

func TestSeededDimSceneRepeatsDecodedPictures(t *testing.T) {
	ffmpeg, _ := qualityTools(t)
	var previous []pictureEvidence
	for run := 0; run < 2; run++ {
		dir := t.TempDir()
		if _, err := generateScene(context.Background(), dir, ffmpeg, "dim-noise"); err != nil {
			t.Fatal(err)
		}
		frames, err := decodedPictureEvidence(context.Background(), dir, filepath.Join(dir, "original.mp4"), ffmpeg)
		if err != nil || len(frames) != 150 {
			t.Fatalf("incomplete repeated scene: %v", err)
		}
		if run > 0 {
			for i := range frames {
				if frames[i] != previous[i] {
					t.Fatalf("fixed-seed generation changed decoded picture or timestamp %d", i)
				}
			}
		}
		previous = frames
	}
}

func TestSceneRejectsUnknownIDAndNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "original.mp4")
	if _, err := generateScene(context.Background(), dir, "must-not-run", "../unknown"); err == nil {
		t.Fatal("unknown scene was accepted")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("unknown scene created a file")
	}
	existing := []byte("preserve this existing original")
	if err := os.WriteFile(file, existing, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := generateScene(context.Background(), dir, "must-not-run", "fast-motion"); err == nil {
		t.Fatal("existing original was accepted as an output")
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(after, existing) {
		t.Fatal("scene generation changed an existing original")
	}
}
