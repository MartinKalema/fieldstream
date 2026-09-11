package lab

type streamObservation struct{ Source, Remote SourceState }

// The configured source limit bounds concurrency. A failed API request should
// cost one timeout for the batch, rather than one timeout per source in series.
func observeSources(settings Settings) map[string]streamObservation {
	type result struct {
		id     string
		remote bool
		state  SourceState
	}
	sources := GetSources(settings)
	results := make(chan result, 2*len(sources))
	for _, source := range sources {
		id := source.ID
		go func() { results <- result{id, false, sourceState(Field, id)} }()
		go func() { results <- result{id, true, sourceState(Central, id)} }()
	}
	observed := map[string]streamObservation{}
	for range 2 * len(sources) {
		item := <-results
		current := observed[item.id]
		if item.remote {
			current.Remote = item.state
		} else {
			current.Source = item.state
		}
		observed[item.id] = current
	}
	return observed
}
