package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/folbricht/desync"
	"github.com/gorilla/mux"
	"github.com/hashicorp/go-uuid"
	"go.uber.org/zap"
)

type dockerUpload struct {
	uuid         string
	rangeStart   int64
	content      *bytes.Buffer
	lastModified time.Time
}

type DockerManifest struct {
	SchemaVersion int64                  `json:"schemaVersion"`
	Config        DockerManifestConfig   `json:"config"`
	Layers        []DockerManifestConfig `json:"layers"`
}

type DockerManifestConfig struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type DockerManifestResponse struct {
	Name          string                          `json:"name"`
	Tag           string                          `json:"tag"`
	Architecture  string                          `json:"architecture"`
	FSLayers      []DockerManifestResponseFSLayer `json:"fsLayers"`
	History       []DockerManifestResponseHistory `json:"history"`
	SchemaVersion int                             `json:"schemaVersion"`
	Signatures    []string                        `json:"signatures"`
}

type DockerManifestResponseFSLayer struct {
	BlobSum string `json:"blobSum"`
}

type DockerManifestResponseHistory struct {
	V1Compatibility string `json:"v1Compatibility"`
}

type dockerHandler struct {
	log       *zap.Logger
	blobs     blobManager
	manifests manifestManager
	uploads   uploadManager
}

func newDockerHandler(logger *zap.Logger, store desync.WriteStore, index desync.IndexWriteStore, r *mux.Router) dockerHandler {
	handler := dockerHandler{
		log:       logger,
		blobs:     newBlobManager(store, index),
		manifests: newManifestManager(store, index),
		uploads:   newUploadManager(store, index),
	}

	r.HandleFunc("/v2/", handler.ping)
	registry := r.PathPrefix("/v2/{name:(?:[a-z0-9]+(?:[._-][a-z0-9]+)*/?){2}}/").Subrouter()
	registry.Methods("GET", "HEAD").Path("/manifests/{reference}").HandlerFunc(handler.manifestGet)
	registry.Methods("PUT").Path("/manifests/{reference}").HandlerFunc(handler.manifestPut)
	registry.Methods("HEAD", "GET").Path("/blobs/{digest:sha256:[a-z0-9]{64}}").HandlerFunc(handler.blobGet)
	registry.Methods("POST").Path("/blobs/uploads/").HandlerFunc(handler.blobUploadPost)
	blobUpload := registry.PathPrefix("/blobs/uploads/{uuid:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}}").Subrouter()
	blobUpload.Methods("GET").HandlerFunc(handler.blobUploadGet)
	blobUpload.Methods("PUT").HandlerFunc(handler.blobUploadPut)
	blobUpload.Methods("PATCH").HandlerFunc(handler.blobUploadPatch)

	return handler
}

func (d dockerHandler) ping(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
	w.Write([]byte(`{}`))
}

func (d dockerHandler) blobUploadPost(w http.ResponseWriter, r *http.Request) {
	u, err := uuid.GenerateUUID()
	if err != nil {
		d.log.Error("Failed to generate UUID", zap.Error(err))
		w.WriteHeader(500)
		return
	}

	d.uploads.new(u)

	h := w.Header()
	h.Set("Content-Length", "0")
	h.Set("Location", r.URL.Host+r.URL.Path+u)
	h.Set("Range", "0-0")
	h.Set("Docker-Upload-UUID", u)
	w.WriteHeader(202)
}

func (d dockerHandler) blobUploadGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	upload := d.uploads.get(vars["uuid"])
	if upload == nil {
		w.WriteHeader(404)
		return
	}

	w.WriteHeader(204)
	h := w.Header()
	h.Set("Content-Length", "0")
	h.Set("Range", fmt.Sprintf("%d-%d", 0, upload.content.Len()))
	h.Set("Docker-Upload-UUID", vars["uuid"])
}

func (d dockerHandler) blobGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	blob, err := d.blobs.get(vars["name"], vars["digest"])
	if blob == nil {
		w.WriteHeader(404)
		return
	}

	if err != nil {
		d.log.Error("getting blob", zap.Error(err))
		w.WriteHeader(404)
		return
	}

	w.WriteHeader(200)
	if r.Method == "GET" {
		w.Write(blob)
	}
}

func (d dockerHandler) manifestPut(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	manifest := &DockerManifest{}
	if err := json.NewDecoder(r.Body).Decode(manifest); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(400)
		w.Write([]byte(`{"errors": [{"code": "MANIFEST_INVALID"}]}`))
		return
	}

	d.manifests.set(vars["name"], vars["reference"], manifest)
	w.WriteHeader(200)
}

func (d dockerHandler) blobUploadPut(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	// TODO: verify digest
	digest := r.URL.Query().Get("digest")
	// parts := strings.SplitN(digest, ":", 2)

	h := w.Header()
	if upload := d.uploads.get(vars["uuid"]); upload != nil {
		_, _ = io.Copy(upload.content, r.Body)

		d.blobs.set(vars["name"], digest, upload.content.Bytes())
		d.uploads.del(vars["uuid"])

		h.Set("Content-Length", "0")
		h.Set("Range", fmt.Sprintf("0-%d", upload.content.Len()))
		h.Set("Docker-Upload-UUID", vars["uuid"])
		w.WriteHeader(201)
	} else {
		h.Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(404)
		w.Write([]byte(`{"errors": [{"code": "BLOB_UPLOAD_UNKNOWN"}]}`))
	}
}

func (d dockerHandler) blobUploadPatch(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	h := w.Header()

	if upload := d.uploads.get(vars["uuid"]); upload != nil {
		_, _ = io.Copy(upload.content, r.Body)

		h.Set("Content-Length", "0")
		h.Set("Location", r.URL.Host+r.URL.Path)
		h.Set("Range", fmt.Sprintf("0-%d", upload.content.Len()))
		h.Set("Docker-Upload-UUID", vars["uuid"])
		w.WriteHeader(204)
	} else {
		h.Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(404)
		w.Write([]byte(`{"errors": [{"code": "BLOB_UPLOAD_UNKNOWN"}]}`))
	}
}

func (d dockerHandler) manifestGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	manifest := d.manifests.get(vars["name"], vars["reference"])
	if manifest == nil {
		w.WriteHeader(404)
		return
	}

	h := w.Header()
	h.Set("Content-Type", manifest.Config.MediaType)
	h.Set("Docker-Content-Digest", manifest.Config.Digest)
	h.Set("Docker-Distribution-Api-Version", "registry/2.0")
	h.Set("Etag", `"`+manifest.Config.Digest+`"`)

	if r.Method == "HEAD" {
		w.WriteHeader(200)
		return
	}

	blob, err := d.blobs.get(vars["name"], manifest.Config.Digest)
	if blob == nil || err != nil {
		w.WriteHeader(404)
		return
	}

	cfg := map[string]interface{}{}
	json.Unmarshal(blob, &cfg)

	fsLayers := []DockerManifestResponseFSLayer{}
	for _, layer := range manifest.Layers {
		fsLayers = append(fsLayers, DockerManifestResponseFSLayer{BlobSum: layer.Digest})
	}

	history := []DockerManifestResponseHistory{}
	for i := range manifest.Layers {
		rootfs, ok := cfg["rootfs"].(map[string]interface{})
		if !ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(500)
			w.Write([]byte(`{"errors": [{"code": "MANIFEST_INVALID"}]}`))
			return
		}

		diffIds, ok := rootfs["diff_ids"].([]interface{})
		if !ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(500)
			w.Write([]byte(`{"errors": [{"code": "MANIFEST_INVALID"}]}`))
			return
		}

		rid := diffIds[i].(string)
		ridp := strings.SplitN(rid, ":", 2)
		entry := map[string]interface{}{
			"created": "1970-01-01T00:00:01+00:00",
			"id":      ridp[1],
		}

		if len(manifest.Layers) > 1 && i != len(manifest.Layers)-1 {
			prid := diffIds[i+1].(string)
			pridp := strings.SplitN(prid, ":", 2)
			entry["parent"] = pridp[1]
		}

		if i == 0 {
			entry["architecture"] = "amd64"
			entry["config"] = cfg["config"]
		}

		if c, err := json.Marshal(entry); err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(400)
			w.Write([]byte(`{"errors": [{"code": "MANIFEST_INVALID"}]}`))
			return
		} else {
			history = append(history, DockerManifestResponseHistory{
				V1Compatibility: string(c),
			})
		}
	}

	res := DockerManifestResponse{
		Name:          vars["name"],
		Tag:           vars["reference"],
		Architecture:  "amd64",
		FSLayers:      fsLayers,
		History:       history,
		SchemaVersion: 1,
		Signatures:    []string{},
	}

	w.WriteHeader(200)
	if err := json.NewEncoder(w).Encode(res); err != nil {
		d.log.Error("Failed to encode JSON", zap.Error(err))
	}
}
