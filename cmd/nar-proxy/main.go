package main

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/jamespfennell/xz"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/numtide/go-nix/nar"
)

const cacheUrl = "https://cache.nixos.org/"

func main() {
	r := mux.NewRouter()
	r.PathPrefix("/nar/{hash:[0-9a-df-np-sv-z]{32}}/").HandlerFunc(narHandler)

	srv := &http.Server{
		Handler:      r,
		Addr:         ":7747",
		ReadTimeout:  time.Minute,
		WriteTimeout: time.Minute,
	}

	if err := srv.ListenAndServe(); err != nil {
		panic(err)
	}
}

func narHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/nar/"+hash)
	narinfoResponse, err := http.Get(cacheUrl + hash + ".narinfo")
	if err != nil {
		panic(err)
	}

	if narinfoResponse.StatusCode != 200 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	narinfo, err := narinfo.Parse(narinfoResponse.Body)
	if err != nil {
		panic(err)
	}

	narResponse, err := http.Get(cacheUrl + narinfo.URL)

	var rd io.Reader
	switch narinfo.Compression {
	case "xz":
		rd = xz.NewReader(narResponse.Body)
	case "none":
		rd = narResponse.Body
	default:
		panic("unknown compression: '" + narinfo.Compression + "'")
	}

	n := nar.NewReader(rd)
	for {
		x, err := n.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			panic(err)
		}

		if x.Name == path || ("/"+x.Name) == path || ("/"+x.Name+"/") == path {
			switch x.Type {
			case "regular":
				mtype := mime.TypeByExtension(filepath.Ext(path))
				w.Header().Set("Content-Type", mtype)
				w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(path)+`"`)
				io.Copy(w, n)
			case "directory":
				entries := listDir(n, filepath.Clean("/"+x.Name))
				w.Write([]byte(strings.TrimSpace(`
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8">
    <title>Directory</title>
  </head>
  <body>
    <table>
			<thead><tr><th>Type</th><th>Path</th><th>Size</th></tr></thead>
			<tbody>
`)))

				for _, entry := range entries {
					eurl := "/nar/" + hash + "/" + entry.Name
					fmt.Fprintf(w, `<tr><td>%s</td><td><a href="%s">%s</a></td><td>%d</td></tr>`, entry.Type, eurl, eurl, entry.Size)
				}

				w.Write([]byte(strings.TrimSpace(`
			</tbody>
    </table>
  </body>
</html>
`)))
			}

			return
		}
	}

	w.WriteHeader(http.StatusNotFound)
}

func listDir(n *nar.Reader, root string) []*nar.Header {
	out := []*nar.Header{}
	for {
		x, err := n.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			panic(err)
		}

		if filepath.Dir(filepath.Clean("/"+x.Name)) == root {
			out = append(out, x)
		}
	}

	return out
}
