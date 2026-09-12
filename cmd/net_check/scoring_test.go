package main

import (
	"fmt"
	"math"
	"testing"
)

func TestScoreWindow20FPSCountsMissingConcealmentAndBoundaries(t *testing.T) {
	w := scoringWindow{FPS: 20, First: 40, Last: 220} // [2,11) seconds.
	reference := map[string]int{}
	c := capture{Timebase: 1.0 / 90000}
	for i := 0; i < 240; i++ {
		h := fmt.Sprint("source-", i)
		reference[h] = i
		if (i >= 40 && i < 43) || (i >= 217 && i < 220) {
			continue
		}
		if i >= 100 && i < 105 {
			h = "concealed"
		}
		c.Frames = append(c.Frames, frame{
			PTS: int64(i-17) * 4500, Hash: h, ArrivalMS: float64(i) * 50,
		})
	}
	q := scoreWindow(c, reference, w)
	if q.Expected != 180 || q.Exact != 169 || q.Missing != 6 || q.Nonmatching != 5 {
		t.Fatalf("missing or concealed frames changed the expected denominator: %+v", q)
	}
	if !q.MappingConsistent || q.Offset != 17 || q.UnlocatedNonmatchingOutputs != 0 || q.ScoredOutputs != 174 {
		t.Fatalf("20 fps PTS mapping failed: %+v", q)
	}
	if math.Abs(q.IntactFraction-169.0/180) > 1e-12 {
		t.Fatalf("intact fraction excluded failed frames: %v", q.IntactFraction)
	}
	if q.LongestNonIntactRunFrames != 5 || q.LongestNonIntactRunMS != 250 || q.IntactArrivalGapsMS.Max != 300 {
		t.Fatalf("20 fps failed-frame run or arrival gap is wrong: %+v", q)
	}
	if q.MaxScheduleDriftMS > 1e-9 {
		t.Fatalf("on-schedule 20 fps frames appear delayed: %v", q.MaxScheduleDriftMS)
	}
}

func TestScoreWindow20FPSMeasuresScheduleDrift(t *testing.T) {
	w := scoringWindow{FPS: 20, First: 40, Last: 220}
	reference := map[string]int{}
	c := capture{Timebase: 1.0 / 20}
	for i := 40; i < 220; i++ {
		h := fmt.Sprint("source-", i)
		reference[h] = i
		arrival := float64(i) * 50
		if i >= 120 {
			arrival += 375
		}
		c.Frames = append(c.Frames, frame{PTS: int64(i), Hash: h, ArrivalMS: arrival})
	}
	q := scoreWindow(c, reference, w)
	if q.Expected != 180 || q.Exact != 180 || q.MaxScheduleDriftMS != 375 || q.ArrivalGapsMS.Max != 425 {
		t.Fatalf("schedule drift did not use 50 ms frame intervals: %+v", q)
	}
}

func TestScoreWindowIncorrectTimestampsKeepExactPixelsAndRejectUnknownMapping(t *testing.T) {
	w := scoringWindow{FPS: 20, First: 40, Last: 220}
	reference := map[string]int{}
	c := capture{Timebase: 1.0 / 20}
	for i := 40; i < 220; i++ {
		h := fmt.Sprint("source-", i)
		reference[h] = i
		pts := int64(i)
		if i%2 == 0 {
			pts += 10
		}
		if i == 63 {
			h = "concealed"
		}
		c.Frames = append(c.Frames, frame{PTS: pts, Hash: h, ArrivalMS: float64(i) * 50})
	}
	q := scoreWindow(c, reference, w)
	if q.MappingConsistent || q.Expected != 180 || q.Exact != 179 || q.Missing != 1 || q.Nonmatching != 0 || q.UnlocatedNonmatchingOutputs != 1 {
		t.Fatalf("bad timestamps discarded exact pixels or invented a concealed-frame position: %+v", q)
	}
	if q.MaxScheduleDriftMS != 0 {
		t.Fatalf("schedule drift should use exact pixel positions: %v", q.MaxScheduleDriftMS)
	}
}

func TestScoreWindowAllFramesMissingKeepFullDuration(t *testing.T) {
	q := scoreWindow(capture{Timebase: 1.0 / 20}, map[string]int{}, scoringWindow{FPS: 20, First: 40, Last: 220})
	if q.Expected != 180 || q.Missing != 180 || q.Exact != 0 || q.IntactFraction != 0 || q.MappingConsistent {
		t.Fatalf("empty capture lost the expected denominator: %+v", q)
	}
	if q.LongestNonIntactRunFrames != 180 || q.LongestNonIntactRunMS != 9000 {
		t.Fatalf("empty capture did not preserve both window boundaries: %+v", q)
	}
}

func TestScoreWindowDuplicateCannotIncreaseIntactFraction(t *testing.T) {
	reference := map[string]int{"first": 40, "last": 219}
	c := capture{Timebase: 1.0 / 20, Frames: []frame{
		{PTS: 40, Hash: "first", ArrivalMS: 2000},
		{PTS: 40, Hash: "first", ArrivalMS: 2010},
		{PTS: 219, Hash: "last", ArrivalMS: 10950},
	}}
	q := scoreWindow(c, reference, scoringWindow{FPS: 20, First: 40, Last: 220})
	if q.Expected != 180 || q.Exact != 2 || q.Missing != 178 || q.DuplicateOutputs != 1 || q.ScoredOutputs != 3 || q.Times[40] != 2000 {
		t.Fatalf("duplicate output inflated exact-frame coverage or replaced its first arrival: %+v", q)
	}
}
