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

const (
	mimeJson = "application/json; charset=utf-8"
)

type dockerUpload struct {
	uuid         string
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

func newDockerHandler(
	logger *zap.Logger,
	store desync.WriteStore,
	index desync.IndexWriteStore,
	manifestDir string,
	r *mux.Router,
) dockerHandler {
	handler := dockerHandler{
		log:       logger,
		blobs:     newBlobManager(store, index),
		manifests: newManifestManager(manifestDir),
		uploads:   newUploadManager(store, index),
	}

	r.HandleFunc("/v2/", handler.ping)

	prefix := "/v2/{name:(?:[a-z0-9]+(?:[._-][a-z0-9]+)*/?){2}}/"
	r.Methods("GET", "HEAD").Path(prefix + "manifests/{reference}").HandlerFunc(handler.manifestGet)
	r.Methods("PUT").Path(prefix + "manifests/{reference}").HandlerFunc(handler.manifestPut)
	r.Methods("GET").Path(prefix + "blobs/{digest:sha256:[a-z0-9]{64}}").HandlerFunc(handler.blobGet)
	r.Methods("HEAD").Path(prefix + "blobs/{digest:sha256:[a-z0-9]{64}}").HandlerFunc(handler.blobHead)
	r.Methods("POST").Path(prefix + "blobs/uploads/").HandlerFunc(handler.blobUploadPost)

	// seems like a bug in mux, we cannot simply use `registry` as our subrouter here
	uploadPrefix := prefix + "blobs/uploads/{uuid:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}}"
	r.PathPrefix(uploadPrefix).Methods("GET").HandlerFunc(handler.blobUploadGet)
	r.PathPrefix(uploadPrefix).Methods("PUT").HandlerFunc(handler.blobUploadPut)
	r.PathPrefix(uploadPrefix).Methods("PATCH").HandlerFunc(handler.blobUploadPatch)

	return handler
}

func (d dockerHandler) ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(headerContentType, mimeJson)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{}`))
}

func (d dockerHandler) blobUploadPost(w http.ResponseWriter, r *http.Request) {
	u, err := uuid.GenerateUUID()
	if err != nil {
		d.log.Error("Failed to generate UUID", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	d.uploads.new(u)

	h := w.Header()
	h.Set("Content-Length", "0")
	h.Set("Location", r.URL.Host+r.URL.Path+u)
	h.Set("Range", "0-0")
	h.Set("Docker-Upload-UUID", u)
	w.WriteHeader(http.StatusAccepted)
}

func (d dockerHandler) blobUploadGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	upload := d.uploads.get(vars["uuid"])
	if upload == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	h := w.Header()
	h.Set("Content-Length", "0")
	h.Set("Range", fmt.Sprintf("%d-%d", 0, upload.content.Len()))
	h.Set("Docker-Upload-UUID", vars["uuid"])
}

func (d dockerHandler) blobHead(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	if err := d.blobs.head(vars["name"], vars["digest"]); err != nil {
		d.log.Error("getting blob", zap.Error(err))
		w.WriteHeader(http.StatusNotFound)
	} else {
		w.WriteHeader(http.StatusOK)
	}
}

func (d dockerHandler) blobGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	blob, err := d.blobs.get(vars["name"], vars["digest"])
	if err != nil {
		d.log.Error("getting blob", zap.Error(err))
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if blob == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(blob)
}

func (d dockerHandler) manifestPut(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	manifest := &DockerManifest{}
	if err := json.NewDecoder(r.Body).Decode(manifest); err != nil {
		fmt.Println(err)
		w.Header().Set(headerContentType, mimeJson)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors": [{"code": "MANIFEST_INVALID"}]}`))
		return
	}

	if manifest.Config.Digest == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors": [{"code": "MANIFEST_INVALID"}]}`))
		return
	}

	if err := d.manifests.set(vars["name"], vars["reference"], manifest); err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors": [{"code": "MANIFEST_INVALID"}]}`))
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (d dockerHandler) blobUploadPut(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	// TODO: verify digest
	digest := r.URL.Query().Get("digest")
	// parts := strings.SplitN(digest, ":", 2)

	h := w.Header()
	if upload := d.uploads.get(vars["uuid"]); upload != nil {
		_, _ = io.Copy(upload.content, r.Body)

		if err := d.blobs.set(vars["name"], digest, upload.content.Bytes()); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			d.log.Error("Failed to store blob", zap.Error(err))
			_, _ = w.Write([]byte(`{"errors": [{"code": "BLOB_UPLOAD_UNKNOWN"}]}`))
		}
		d.uploads.del(vars["uuid"])

		h.Set("Content-Length", "0")
		h.Set("Range", fmt.Sprintf("0-%d", upload.content.Len()))
		h.Set("Docker-Upload-UUID", vars["uuid"])
		w.WriteHeader(http.StatusCreated)
	} else {
		h.Set(headerContentType, mimeJson)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors": [{"code": "BLOB_UPLOAD_UNKNOWN"}]}`))
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
		w.WriteHeader(http.StatusNoContent)
	} else {
		h.Set(headerContentType, mimeJson)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors": [{"code": "BLOB_UPLOAD_UNKNOWN"}]}`))
	}
}

func (d dockerHandler) manifestGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	manifest, err := d.manifests.get(vars["name"], vars["reference"])
	if err != nil {
		fmt.Println(err)
		d.log.Error("getting manifest", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if manifest == nil {
		fmt.Println("404")
		d.log.Warn("manifest not found")
		w.WriteHeader(http.StatusNotFound)
		return
	}

	h := w.Header()
	h.Set(headerContentType, manifest.Config.MediaType)
	h.Set("Docker-Content-Digest", manifest.Config.Digest)
	h.Set("Docker-Distribution-Api-Version", "registry/2.0")
	h.Set("Etag", `"`+manifest.Config.Digest+`"`)

	if r.Method == "HEAD" {
		w.WriteHeader(http.StatusOK)
		return
	}

	blob, err := d.blobs.get(vars["name"], manifest.Config.Digest)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if blob == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	cfg := map[string]interface{}{}
	if err := json.Unmarshal(blob, &cfg); err != nil {
		d.log.Error("unmarshal manifest", zap.Error(err))
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors": [{"code": "MANIFEST_INVALID"}]}`))
		return
	}

	fsLayers := []DockerManifestResponseFSLayer{}
	for _, layer := range manifest.Layers {
		fsLayers = append(fsLayers, DockerManifestResponseFSLayer{BlobSum: layer.Digest})
	}

	history := []DockerManifestResponseHistory{}
	for i := range manifest.Layers {
		rootfs, ok := cfg["rootfs"].(map[string]interface{})
		if !ok {
			w.Header().Set(headerContentType, mimeJson)
			d.log.Error("manifest invalid", zap.Error(err))
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors": [{"code": "MANIFEST_INVALID"}]}`))
			return
		}

		diffIds, ok := rootfs["diff_ids"].([]interface{})
		if !ok {
			w.Header().Set(headerContentType, mimeJson)
			d.log.Error("manifest invalid", zap.Error(err))
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors": [{"code": "MANIFEST_INVALID"}]}`))
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
			w.Header().Set(headerContentType, mimeJson)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors": [{"code": "MANIFEST_INVALID"}]}`))
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

	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(res); err != nil {
		d.log.Error("Failed to encode JSON", zap.Error(err))
	}
}
