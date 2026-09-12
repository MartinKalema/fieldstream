package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"fieldvideolab/internal/lab"
)

func sourceID(p lab.Paths, requested string) (string, error) {
	settings, err := p.LoadSettings()
	if err != nil {
		return "", err
	}
	sources := lab.GetSources(settings)
	if requested == "" && len(sources) > 0 {
		return sources[0].ID, nil
	}
	for _, source := range sources {
		if source.ID == requested {
			return source.ID, nil
		}
	}
	return "", fmt.Errorf("unknown source %q; use ./lab source list", requested)
}

func printViewers(p lab.Paths) error {
	settings, err := p.LoadSettings()
	if err != nil {
		return err
	}
	for _, source := range lab.GetSources(settings) {
		fmt.Printf("%s (%s)\n  Watch locally: ./lab --source %s view local\n  Watch forwarded: ./lab --source %s view forwarded\n", source.ID, source.Label, source.ID, source.ID)
	}
	return nil
}

func printStatus(state lab.State, filter string) {
	label := func(ok bool, yes, no string) string {
		if ok {
			return yes
		}
		return no
	}
	fmt.Println("Controller:", label(state.Running, "running", "stopped"))
	ids := make([]string, 0, len(state.Sources))
	for id := range state.Sources {
		if filter == "" || id == filter {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		stream := state.Sources[id]
		fmt.Printf("\n%s (%s)\n", id, stream.Label)
		if state.Running && !stream.Source.Observed {
			fmt.Println("  Camera connection: unknown — status check failed")
		} else {
			fmt.Println("  Camera connection:", label(stream.Source.Ready, "connected", "waiting for video"))
		}
		if state.Running && !stream.Remote.Observed {
			fmt.Println("  Forwarded video: unknown — status check failed")
		} else {
			fmt.Println("  Forwarded video:", label(stream.Remote.Ready, "available", "not available"))
		}
		fmt.Println("  Recorder:", label(stream.Recording.Running, "running", "waiting or paused"))
		fmt.Printf("  Completed pieces: %d (%.1f MB)\n  Remote picture setting: %s\n", stream.Recording.Segments, float64(stream.Recording.Bytes)/1e6, stream.Profile)
		waitMS := stream.RelayWaitMS
		if waitMS == 0 {
			waitMS = 300 // Status written by an older controller.
		}
		if stream.RelayLink == "local" {
			fmt.Println("  Forwarding link: local — direct connection between programs on this Mac")
		} else {
			fmt.Printf("  Forwarding link: SRT; recovery wait: %d milliseconds (requested)\n", waitMS)
		}
		fmt.Println("  Test pattern:", label(state.Running && stream.DemoEnabled, "on — generated footage", "off"))
		if stream.Recording.BlockedReason != nil {
			fmt.Println(" ", *stream.Recording.BlockedReason)
		}
		for role, worker := range stream.Workers {
			if worker.LogError != "" {
				fmt.Println("  Logging unavailable for", role+":", worker.LogError)
			}
		}
	}
	fmt.Printf("\nTotal local recordings: %d pieces, %.1f MB\n", state.Recording.Segments, float64(state.Recording.Bytes)/1e6)
	for _, warning := range state.Recording.Warnings {
		fmt.Println("Recording warning:", warning)
	}
	if state.Archive.Enabled {
		fmt.Printf("R2 archive: %d confirmed, %d pending (%.1f MB); fixed upload limit %.0f KB/s\n", state.Archive.Summary.Archived, state.Archive.Summary.Pending, float64(state.Archive.Summary.PendingBytes)/1e6, float64(state.Archive.BytesPerSecond)/1000)
		if !state.Running {
			fmt.Println("Uploads resume when the lab starts.")
		}
	} else {
		fmt.Println("R2 archive: disabled — recordings stay on this Mac")
	}
	if state.Archive.LastError != "" {
		fmt.Println("Archive:", state.Archive.LastError)
	}
	if state.Archive.Summary.PendingMissing > 0 {
		fmt.Printf("Warning: %d recordings have no local file and no confirmed archive copy.\n", state.Archive.Summary.PendingMissing)
	}
	if state.HealthNote != "" {
		fmt.Println(state.HealthNote)
	}
	fmt.Println(state.MeasurementNote)
}

func run() error {
	flags := flag.NewFlagSet("videolab", flag.ContinueOnError)
	root := flags.String("root", ".", "project folder")
	selected := flags.String("source", "", "source ID for a control command (default: first configured source)")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	absolute, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	p := lab.NewPaths(absolute)
	args := flags.Args()
	if len(args) == 0 {
		fmt.Println("Commands: setup, source list|add|guide, start, view [local|forwarded], status, stop, demo, demo-stop, relay-off, relay-on, relay-link local|srt, relay-wait 120|300, profile copy|detail|small, recordings check|health, archive-config, test, logs")
		fmt.Println("Watch video: ./lab --source camera-01 view local (GStreamer; close its window or press Ctrl+C to stop viewing)")
		fmt.Println("Choose a source: ./lab --source camera-02 profile small")
		return nil
	}
	switch args[0] {
	case "view":
		return viewCommand(p, *selected, args[1:], os.Stdout, os.Stderr)
	case "recordings":
		return recordingsCommand(p, *selected, args[1:], os.Stdout)
	case "setup":
		setupFlags := flag.NewFlagSet("setup", flag.ContinueOnError)
		host := setupFlags.String("host", "", "Mac's local network IPv4 address")
		if err := setupFlags.Parse(args[1:]); err != nil {
			return err
		}
		if err := p.WithStoppedConfiguration(func() error { _, e := p.Setup(*host); return e }); err != nil {
			return err
		}
		fmt.Println("Source connections prepared. Private connection files are listed in:", filepath.Join(p.Root, "SOURCES.txt"))
		return printViewers(p)
	case "source":
		if len(args) < 2 {
			return errors.New("use source list, source add ID --label NAME, or source guide ID")
		}
		switch args[1] {
		case "list":
			if err := printViewers(p); err != nil {
				return err
			}
			fmt.Println("Connection guide index:", filepath.Join(p.Root, "SOURCES.txt"))
			return nil
		case "add":
			if len(args) < 3 {
				return errors.New("use ./lab source add camera-02 --label 'Second camera'")
			}
			addFlags := flag.NewFlagSet("source add", flag.ContinueOnError)
			label := addFlags.String("label", args[2], "display name")
			if err := addFlags.Parse(args[3:]); err != nil {
				return err
			}
			if len(addFlags.Args()) != 0 {
				return errors.New("unexpected source arguments")
			}
			if err := p.WithStoppedConfiguration(func() error { return p.AddSource(args[2], *label) }); err != nil {
				return err
			}
			fmt.Printf("Added source %s. Private guide: %s\n", args[2], filepath.Join(p.Local, "connections", args[2]+".txt"))
			return nil
		case "guide":
			if len(args) != 3 {
				return errors.New("use ./lab source guide SOURCE_ID")
			}
			id, err := sourceID(p, args[2])
			if err != nil {
				return err
			}
			fmt.Println("Private connection guide:", filepath.Join(p.Local, "connections", id+".txt"))
			return nil
		default:
			return errors.New("source command must be list, add or guide")
		}
	case "start":
		if err := p.Start(); err != nil {
			return err
		}
		fmt.Println("Video services started. Waiting for your configured sources.")
		return printViewers(p)
	case "stop":
		if err := p.Stop(); err != nil {
			return err
		}
		fmt.Println("Stopped. Your recordings have been kept.")
		return nil
	case "status":
		if *selected != "" {
			if _, err := sourceID(p, *selected); err != nil {
				return err
			}
		}
		state := p.Status()
		if len(args) > 1 && args[1] == "--json" {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(state)
		}
		printStatus(state, *selected)
		return nil
	case "archive-config":
		name, err := p.WriteR2Template()
		if err != nil {
			return err
		}
		fmt.Println("Private R2 configuration:", name)
		fmt.Println("Uploads start only when enabled is true and the lab is restarted. Existing credentials are kept.")
		return nil
	case "_supervise":
		if len(args) != 2 {
			return errors.New("supervisor token required")
		}
		return p.Supervise(args[1])
	case "logs":
		output, err := p.SafeLogs()
		if err != nil {
			return err
		}
		fmt.Print(output)
		return nil
	case "test":
		return lab.RunIntegration(p)
	case "demo", "demo-stop", "relay-off", "relay-on", "profile", "relay-wait", "relay-link":
		if !p.Running() {
			return errors.New("start the lab first: ./lab start")
		}
		id, err := sourceID(p, *selected)
		if err != nil {
			return err
		}
		switch args[0] {
		case "demo":
			stream := p.Status().Sources[id]
			if stream.Source.Ready && !stream.DemoEnabled {
				return errors.New("a camera is connected to this source; stop it before starting its test pattern")
			}
			err = p.ChangeSourceControl(id, func(c *lab.Control) { c.DemoEnabled = true })
			if err == nil {
				fmt.Println(id + ": generated test pattern requested.")
			}
		case "demo-stop":
			err = p.ChangeSourceControl(id, func(c *lab.Control) { c.DemoEnabled = false })
			if err == nil {
				fmt.Println(id + ": test pattern stopped; a camera can now connect.")
			}
		case "relay-off", "relay-on":
			on := args[0] == "relay-on"
			err = p.ChangeSourceControl(id, func(c *lab.Control) { c.RelayEnabled = on })
			if err == nil {
				if on {
					fmt.Println(id + ": forwarding resumed.")
				} else {
					fmt.Println(id + ": forwarding paused; local viewing and recording continue.")
				}
			}
		case "relay-wait":
			if len(args) != 2 || (args[1] != "120" && args[1] != "300") {
				return errors.New("use ./lab --source SOURCE_ID relay-wait 120 or 300 (milliseconds)")
			}
			waitMS, _ := strconv.Atoi(args[1])
			err = p.ChangeSourceControl(id, func(c *lab.Control) { c.RelayWaitMS = waitMS })
			if err == nil {
				fmt.Printf("%s: SRT recovery wait saved as %d milliseconds; applies when the SRT forwarding link is selected.\n", id, waitMS)
			}
		case "relay-link":
			if len(args) != 2 || (args[1] != "local" && args[1] != "srt") {
				return errors.New("use ./lab --source SOURCE_ID relay-link local or srt")
			}
			err = p.ChangeSourceControl(id, func(c *lab.Control) { c.RelayLink = args[1] })
			if err == nil {
				fmt.Printf("%s: forwarding link set to %s; its forwarded picture will briefly reconnect.\n", id, args[1])
			}
		case "profile":
			if len(args) != 2 || (args[1] != "copy" && args[1] != "detail" && args[1] != "small") {
				return errors.New("use ./lab --source SOURCE_ID profile copy, detail, or small")
			}
			err = p.ChangeSourceControl(id, func(c *lab.Control) { c.Profile = args[1] })
			if err == nil {
				fmt.Println(id+": remote picture setting", args[1]+"; its viewer will briefly reconnect.")
			}
		}
		return err
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Could not complete that step:", err)
		os.Exit(1)
	}
}
