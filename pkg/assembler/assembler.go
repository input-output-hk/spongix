package assembler

import (
	"bytes"
	"io"

	"github.com/folbricht/desync"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/pkg/errors"
)

type Assembler struct {
	store      desync.Store
	index      desync.Index
	idx        int
	data       *bytes.Buffer
	readBytes  int64
	wroteBytes int64
}

func NewAssembler(store desync.Store, index desync.Index) *Assembler {
	return &Assembler{store: store, index: index, data: &bytes.Buffer{}}
}

func (a *Assembler) Close() error { return nil }

func (a *Assembler) Read(p []byte) (int, error) {
	if a.data.Len() > 0 {
		writeBytes, _ := a.data.Read(p)
		a.wroteBytes += int64(writeBytes)
		return writeBytes, nil
	}

	if a.idx >= len(a.index.Chunks) {
		if a.wroteBytes != a.index.Length() {
			return 0, errors.New("written bytes don't match index length")
		}
		if a.wroteBytes != a.readBytes {
			return 0, errors.New("read and written bytes are different")
		}
		return 0, io.EOF
	}

	if chunk, err := a.store.GetChunk(a.index.Chunks[a.idx].ID); err != nil {
		return 0, err
	} else if data, err := chunk.Data(); err != nil {
		return 0, err
	} else {
		readBytes, _ := a.data.Write(data)
		a.readBytes += int64(readBytes)
		writeBytes, _ := a.data.Read(p)
		a.wroteBytes += int64(writeBytes)
		a.idx++
		return writeBytes, nil
	}
}

var _ = io.Reader(&Assembler{})

// very simple implementation, mostly used for assembling narinfo which is
// usually tiny to avoid overhead of creating files.
func Assemble(store desync.Store, index desync.Index) io.ReadCloser {
	return NewAssembler(store, index)
}

func AssembleNarinfo(store desync.Store, index desync.Index) (*narinfo.NarInfo, error) {
	buf := Assemble(store, index)

	info, err := narinfo.Parse(buf)
	if err != nil {
		return info, errors.WithMessage(err, "while unmarshaling narinfo")
	}

	return info, nil
}
