package main

import (
	"fmt"
	"testing"
)

func TestScoreCountsConcealmentMissingAndBoundaries(t *testing.T) {
	hashes := map[string]int{}
	c := capture{Timebase: 1.0 / 30}
	for i := 0; i < 360; i++ {
		h := fmt.Sprint("source-", i)
		hashes[h] = i
		if (i >= 60 && i < 63) || (i >= 327 && i < 330) {
			continue
		}
		if i >= 100 && i < 105 {
			h = "concealed"
		}
		c.Frames = append(c.Frames, frame{PTS: int64(i - 20), Hash: h, ArrivalMS: float64(i) * 1000 / 30})
	}
	q := score(c, hashes)
	if q.Exact != 259 || q.Missing != 6 || q.Nonmatching != 5 || !q.MappingConsistent {
		t.Fatalf("wrong quality: %+v", q)
	}
	if q.LongestNonIntactRunFrames != 5 || q.IntactArrivalGapsMS.Max < 199 {
		t.Fatalf("concealed frames hid a gap: %+v", q)
	}
}

func TestUniqueHashesRemainExactWhenTimestampsAreInconsistent(t *testing.T) {
	hashes := map[string]int{}
	c := capture{Timebase: 1.0 / 30}
	for i := 60; i < 330; i++ {
		h := fmt.Sprint(i)
		hashes[h] = i
		pts := int64(i)
		if i%2 == 0 {
			pts += 10
		}
		c.Frames = append(c.Frames, frame{PTS: pts, Hash: h, ArrivalMS: float64(i) * 1000 / 30})
	}
	q := score(c, hashes)
	if q.Exact != 270 || q.MappingConsistent || q.Missing != 0 {
		t.Fatalf("exact pixels must not be discarded by bad PTS: %+v", q)
	}
}
