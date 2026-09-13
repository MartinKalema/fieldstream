package main

import (
	"context"
	"strings"
	"testing"
)

func TestBandwidthTrialSetupFailureKeepsPlannedDenominator(t *testing.T) {
	r := runTrial(context.Background(), t.TempDir(), "/missing-test-ffmpeg", "/missing-test-receiver", "/missing-test-clip", profile{Name: "test"}, 120, 17, nil, scoringWindow{FPS: 20, First: 40, Last: 220})
	if r.Error == "" || r.TimingConclusive || r.Reference.Expected != 180 || r.Impaired.Expected != 180 || r.Timely250Fraction != 0 {
		t.Fatalf("failed setup must stay visibly inconclusive with its planned denominator: %+v", r)
	}
}

func TestBandwidthOptionsBoundWorkAndRejectAmbiguousRuns(t *testing.T) {
	o, err := parseBandwidthOptions("0,1500,900", 32768, "copy,detail20,small20", "17,41")
	if err != nil || len(o.Rates)*len(o.Encodings)*len(o.Repeats) != 18 {
		t.Fatalf("default matrix: %+v, %v", o, err)
	}
	for _, tc := range []struct {
		rates, encodings, repeats string
		queue                     int
	}{
		{"", "copy", "17", 32768}, {"-1", "copy", "17", 32768},
		{"63", "copy", "17", 32768}, {"100001", "copy", "17", 32768},
		{"0,0", "copy", "17", 32768}, {"0", "copy,copy", "17", 32768},
		{"0", "camera-file", "17", 32768}, {"0", "", "17", 32768},
		{"0", "copy", "17,17", 32768}, {"0", "copy", "1,2,3,4", 32768},
		{"0", "copy", "-1", 32768}, {"0", "copy", "17", 2047},
		{"0", "copy", "17", 1048577}, {"0,900,1200,1500,2000", "copy,detail20,small20", "1,2,3", 32768},
	} {
		if _, err := parseBandwidthOptions(tc.rates, tc.queue, tc.encodings, tc.repeats); err == nil {
			t.Errorf("accepted invalid matrix: %+v", tc)
		}
	}
}

func TestBandwidthEncodingsKeepSharedSourceAndDistinctFrameRates(t *testing.T) {
	for _, name := range []string{"copy", "detail20", "small20"} {
		args, e := bandwidthEncodeArgs(name, "/private-test/source.mp4", "/private-test/output.mp4")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-n ") || !strings.Contains(joined, "-bf 0") || args[len(args)-1] != "/private-test/output.mp4" {
			t.Fatalf("unsafe or delayed encoding: %q", joined)
		}
		if name == "copy" {
			if e.FPS != 30 || e.Frames != 360 || !strings.Contains(joined, "testsrc2=size=1280x720:rate=30") {
				t.Fatalf("unexpected shared source: %+v %q", e, joined)
			}
		} else {
			if e.FPS != 20 || e.Frames != 240 || !strings.Contains(joined, "-i /private-test/source.mp4") || !strings.Contains(joined, "fps=20") || strings.Contains(joined, "testsrc2") {
				t.Fatalf("candidate must derive from shared source at20fps: %+v %q", e, joined)
			}
		}
		if name == "small20" && (e.Width != 640 || e.Height != 360 || e.TargetKbps != 650 || !strings.Contains(joined, "scale=640:360:flags=bicubic")) {
			t.Fatalf("incorrect smaller candidate: %+v %q", e, joined)
		}
		if name == "detail20" && (e.Width != 1280 || e.Height != 720 || e.TargetKbps != 1200 || strings.Contains(joined, "scale=")) {
			t.Fatalf("detail candidate changed dimensions: %+v %q", e, joined)
		}
	}
}

func TestBandwidthRuleRequiresCompleteAndTimelyDelivery(t *testing.T) {
	good := trial{TimingConclusive: true, Timely250Fraction: 1, Impaired: quality{Expected: 180, Exact: 180, IntactArrivalGapsMS: distribution{Count: 179, Max: 80}}}
	if !bandwidthUseful(good) {
		t.Fatal("complete, timely and measured delivery should meet the rule")
	}
	for _, change := range []func(*trial){
		func(r *trial) { r.TimingConclusive = false },
		func(r *trial) { r.Error = "publisher failed" },
		func(r *trial) { r.Impaired.Expected = 0; r.Impaired.Exact = 0 },
		func(r *trial) { r.Impaired.Exact--; r.Impaired.Missing++ },
		func(r *trial) { r.Impaired.DuplicateOutputs++ },
		func(r *trial) { r.Timely250Fraction = .98 },
		func(r *trial) { r.Impaired.IntactArrivalGapsMS.Max = 151 },
	} {
		r := good
		change(&r)
		if bandwidthUseful(r) {
			t.Errorf("accepted incomplete or late result: %+v", r)
		}
	}
	bad := trial{Name: "no-pictures", Impaired: quality{Expected: 180}}
	text := bandwidthMarkdown(bandwidthReport{Results: []bandwidthResult{{Encoding: "detail20", Trial: bad}}})
	if !strings.Contains(text, "Inconclusive") || strings.Contains(text, "0.0 ms") || !strings.Contains(text, "0/180") {
		t.Fatalf("missing timing must not read as zero-delay success: %s", text)
	}
}
