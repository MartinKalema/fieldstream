package main

import (
	"net/http"
	"regexp"

	"fieldvideolab/internal/viewer"
)

var sourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func whepProxy(sourceIDs []string, client *http.Client) http.Handler {
	return viewer.WHEPProxy(sourceIDs, 18889, 28889, client)
}
