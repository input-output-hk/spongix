package main

const (
	headerContentType   = "Content-Type"
	headerContentLength = "Content-Length"
	headerLocation      = "Location"

	headerCache       = "X-Cache"
	headerCacheHit    = "HIT"
	headerCacheRemote = "REMOTE"
	headerCacheMiss   = "MISS"

	headerCacheUpstream = "X-Cache-Upstream"
)

type cacheRequest struct {
	namespace, url, location string
}

type Realisation struct {
	DependentRealisations map[string]string `json:"dependentRealisations"`
	ID                    string            `json:"id"`
	OutPath               string            `json:"outPath"`
	Signatures            []string          `json:"signatures"`
}
