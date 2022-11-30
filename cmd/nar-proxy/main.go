package main

import (
	"compress/bzip2"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/andybalholm/brotli"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/input-output-hk/spongix/pkg/logger"
	"github.com/jamespfennell/xz"
	"github.com/klauspost/compress/zstd"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/numtide/go-nix/nar"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

type NarProxy struct {
	log      *zap.Logger
	CacheUrl string `arg:"--cache-url,env:CACHE_URL" help:"upstream cache URL"`
	Prefix   string `arg:"--prefix,env:PREFIX" help:"use this url prefix for routing"`
	LogLevel string `arg:"--log-level,env:LOG_LEVEL" help:"One of debug, info, warn, error, dpanic, panic, fatal"`
	LogMode  string `arg:"--log-mode,env:LOG_MODE" help:"development or production"`
	Listen   string `arg:"--listen,env:LISTEN_ADDR" help:"Listen on this address"`
}

func main() {
	np := NewNarProxy()
	arg.MustParse(np)
	np.setupLogger()
	np.CacheUrl = strings.TrimSuffix(np.CacheUrl, "/") + "/"
	np.Prefix = strings.TrimSuffix(np.Prefix, "/") + "/"
	np.Start()
}

func NewNarProxy() *NarProxy {
	devLog, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}

	return &NarProxy{
		CacheUrl: "https://cache.iog.io/",
		Listen:   ":7747",
		LogLevel: "debug",
		LogMode:  "production",
		Prefix:   "/dl",
		log:      devLog,
	}
}

func (np *NarProxy) setupLogger() {
	if log, err := logger.SetupLogger(np.LogMode, np.LogLevel); err != nil {
		panic(err)
	} else {
		np.log = log
	}
}

func (np *NarProxy) Start() {
	srv := &http.Server{
		Handler:      np.newRouter(),
		Addr:         np.Listen,
		ReadTimeout:  time.Minute,
		WriteTimeout: time.Minute,
	}

	np.log.Info("Server starting", zap.String("listen", np.Listen))
	if err := srv.ListenAndServe(); err != nil {
		panic(err)
	}
}

func (np *NarProxy) newRouter() *mux.Router {
	r := mux.NewRouter()
	r.Use(handlers.RecoveryHandler(handlers.PrintRecoveryStack(true)))
	r.PathPrefix(np.Prefix + "{hash:[0-9a-df-np-sv-z]{32}}{name:-[^/]+}").HandlerFunc(np.narHandler)
	return r
}

func (np *NarProxy) narHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]
	name := vars["name"]
	path := strings.TrimPrefix(strings.TrimPrefix(r.URL.EscapedPath(), np.Prefix+hash+name), "/")
	np.log.Debug("serving", zap.String("url", r.URL.EscapedPath()))

	narinfoResponse, err := http.Get(np.CacheUrl + hash + ".narinfo")
	if err != nil || narinfoResponse.StatusCode != 200 {
		w.WriteHeader(narinfoResponse.StatusCode)
		_, _ = io.Copy(w, narinfoResponse.Body)
		return
	}

	narinfo, err := narinfo.Parse(narinfoResponse.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, errors.WithMessage(err, "parsing narinfo").Error())
		return
	}

	narResponse, err := http.Get(np.CacheUrl + narinfo.URL)
	if err != nil || narResponse.StatusCode != 200 {
		w.WriteHeader(narinfoResponse.StatusCode)
		_, _ = io.Copy(w, narinfoResponse.Body)
		return
	}

	var rd io.Reader
	switch narinfo.Compression {
	case "br":
		rd = brotli.NewReader(narResponse.Body)
	case "bzip2":
		rd = bzip2.NewReader(narResponse.Body)
	case "none":
		rd = narResponse.Body
	case "xz":
		rd = xz.NewReader(narResponse.Body)
	case "zstd":
		rd, err = zstd.NewReader(narResponse.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, errors.WithMessage(err, "creating zstd reader").Error())
			return
		}
	default:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "unknown NAR compression: '"+narinfo.Compression+"'")
		return
	}

	symlink := ""
	nrd := nar.NewReader(rd)
	for {
		x, err := nrd.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, errors.WithMessage(err, "iterating NAR").Error())
			return
		}

		if (symlink != "" && nameMatches(x.Name, symlink)) || nameMatches(x.Name, path) {
			switch x.Type {
			case nar.TypeSymlink:
				// TODO: ensure regular files always come after symlinks
				rel := filepath.Join(filepath.Dir(x.Name), x.Linkname)
				symlink = rel
			case nar.TypeRegular:
				mtype := mime.TypeByExtension(filepath.Ext(path))
				if mtype == "" {
					mtype = "application/octet-stream"
				}
				w.Header().Set("Content-Type", mtype)
				w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(path)+`"`)
				w.Header().Set("Cache-Control", "public")
				w.Header().Set("Content-Length", fmt.Sprintf("%d", x.Size))
				w.Header().Set("Expires", (time.Now().Add(time.Hour * 24 * 30)).Format(time.RFC1123))

				_, _ = io.Copy(w, nrd)
				return
			case nar.TypeDirectory:
				_, _ = w.Write([]byte(strings.TrimSpace(`
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

				entries, err := listDir(nrd, x.Name)
				if err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = io.WriteString(w, errors.WithMessage(err, "listing dir").Error())
					return
				}

				for _, entry := range entries {
					eurl := np.Prefix + hash + name + "/" + entry.Name
					fmt.Fprintf(w, `<tr><td>%s</td><td><a href="%s">%s</a></td><td>%d</td></tr>`, entry.Type, eurl, eurl, entry.Size)
				}

				_, _ = io.WriteString(w, strings.TrimSpace(`
      </tbody>
    </table>
  </body>
</html>
				 `))
				return
			case nar.TypeUnknown:
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, "unknown type for NAR header")
				return
			}
		}
	}

	w.WriteHeader(http.StatusNotFound)
}

func nameMatches(a, b string) bool {
	return (a == b) || (a+"/" == b)
}

func listDir(n *nar.Reader, root string) ([]*nar.Header, error) {
	if root == "" {
		root = "."
	}

	out := []*nar.Header{}
	for {
		x, err := n.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, errors.WithMessage(err, "getting next NAR header")
		}

		if filepath.Dir(x.Name) == root {
			out = append(out, x)
		}
	}

	return out, nil
}

// TODO: using the `.ls` API only works when the NAR is uncompressed!
// func readls() {
// 	if fd, err := os.Open("jmgzcgzb7hfd94k04hppq600sqjl0dla.ls"); err != nil {
// 		panic(err)
// 	} else {
// 		rd := brotli.NewReader(fd)
// 		l := &ls{}
// 		dec := json.NewDecoder(rd)
// 		dec.DisallowUnknownFields()
// 		if err = dec.Decode(l); err != nil {
// 			panic(err)
// 		} else {
// 			if l.Version != 1 {
// 				fmt.Println("warning: ls is not version 1")
// 			}
//
// 			y := deepGet(l.Root, "include", "libssh", "callbacks.h")
// 			pretty.Println(y)
// 			narinfoRes, err := http.Get(cacheUrl + "jmgzcgzb7hfd94k04hppq600sqjl0dla.narinfo")
// 			if err != nil {
// 				panic(err)
// 			}
// 			info, err := narinfo.Parse(narinfoRes.Body)
// 			if err != nil {
// 				panic(err)
// 			}
// 			narReq, err := http.NewRequest("GET", cacheUrl+info.URL, nil)
// 			if err != nil {
// 				panic(err)
// 			}
// 			narReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", y.NarOffset, y.Size))
// 			narRes, err := http.DefaultClient.Do(narReq)
// 			if err != nil {
// 				panic(err)
// 			}
// 			out, err := os.Create("out")
// 			if err != nil {
// 				panic(err)
// 			}
// 			io.Copy(out, narRes.Body)
// 		}
// 	}
// }
//
// func deepGet(entry *lsEntry, keys ...string) *lsEntry {
// 	if len(keys) == 0 {
// 		return nil
// 	}
//
// 	if child, found := entry.Entries[keys[0]]; found {
// 		if len(keys) == 1 {
// 			return child
// 		} else {
// 			return deepGet(child, keys[1:]...)
// 		}
// 	}
//
// 	return nil
// }
//
// type ls struct {
// 	Version int
// 	Root    *lsEntry
// }
//
// type lsEntry struct {
// 	Type       string
// 	Size       int64
// 	Executable bool
// 	NarOffset  int64 `json:"narOffset"`
// 	Entries    map[string]*lsEntry
// 	Target     string
// }
