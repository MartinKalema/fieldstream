package main

import (
	"math"
	"sort"
)

// scoringWindow uses source frame indices so every expected frame remains in
// the denominator, including frames missing at the beginning or end.
type scoringWindow struct {
	FPS   int `json:"fps"`
	First int `json:"first_inclusive"`
	Last  int `json:"last_exclusive"`
}

func score(c capture, reference map[string]int) quality {
	return scoreWindow(c, reference, scoringWindow{FPS: fps, First: firstScored, Last: lastScored})
}

func scoreWindow(c capture, reference map[string]int, w scoringWindow) quality {
	q := quality{Expected: w.Last - w.First, Times: map[int]float64{}}
	offsets := map[int]int{}
	for _, f := range c.Frames {
		if i, ok := reference[f.Hash]; ok {
			offsets[i-int(math.Round(float64(f.PTS)*c.Timebase*float64(w.FPS)))]++
		}
	}
	best := 0
	for o, n := range offsets {
		if n > best || (n == best && o < q.Offset) {
			best = n
			q.Offset = o
		}
	}
	q.MappingConsistent = len(offsets) == 1 && best > 0
	seen := map[int]int{}
	bad := map[int]bool{}
	var gaps []float64
	var previous, firstArrival float64
	firstIndex := -1
	for _, f := range c.Frames {
		i := int(math.Round(float64(f.PTS)*c.Timebase*float64(w.FPS))) + q.Offset
		source, exact := reference[f.Hash]
		if exact {
			i = source // Unique pixel identity is authoritative even if PTS mapping fails.
		} else if !q.MappingConsistent {
			q.UnlocatedNonmatchingOutputs++
			continue
		}
		if i < w.First || i >= w.Last {
			continue
		}
		q.ScoredOutputs++
		seen[i]++
		if seen[i] > 1 {
			q.DuplicateOutputs++
		}
		if exact {
			if _, ok := q.Times[i]; !ok {
				q.Times[i] = f.ArrivalMS
			}
		} else {
			bad[i] = true
		}
		if firstIndex < 0 {
			firstIndex = i
			firstArrival = f.ArrivalMS
		} else {
			gaps = append(gaps, f.ArrivalMS-previous)
		}
		drift := (f.ArrivalMS - firstArrival) - float64(i-firstIndex)*1000/float64(w.FPS)
		q.MaxScheduleDriftMS = math.Max(q.MaxScheduleDriftMS, math.Abs(drift))
		previous = f.ArrivalMS
	}
	run := 0
	var intactTimes []float64
	for i := w.First; i < w.Last; i++ {
		if _, ok := q.Times[i]; ok {
			q.Exact++
			run = 0
			intactTimes = append(intactTimes, q.Times[i])
		} else if bad[i] {
			q.Nonmatching++
			run++
		} else {
			q.Missing++
			run++
		}
		q.LongestNonIntactRunFrames = max(q.LongestNonIntactRunFrames, run)
	}
	sort.Float64s(intactTimes)
	var intactGaps []float64
	for i := 1; i < len(intactTimes); i++ {
		intactGaps = append(intactGaps, intactTimes[i]-intactTimes[i-1])
	}
	q.IntactArrivalGapsMS = dist(intactGaps)
	q.LongestNonIntactRunMS = float64(q.LongestNonIntactRunFrames) * 1000 / float64(w.FPS)
	q.IntactFraction = float64(q.Exact) / float64(q.Expected)
	q.ArrivalGapsMS = dist(gaps)
	return q
}
