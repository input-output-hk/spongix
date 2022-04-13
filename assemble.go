package main

import (
	"bytes"
	"io"

	"github.com/folbricht/desync"
	"github.com/pkg/errors"
)

type assembler struct {
	store      desync.Store
	index      desync.Index
	idx        int
	data       *bytes.Buffer
	readBytes  int64
	wroteBytes int64
}

func newAssembler(store desync.Store, index desync.Index) *assembler {
	return &assembler{store: store, index: index, data: &bytes.Buffer{}}
}

func (a *assembler) Close() error { return nil }

func (a *assembler) Read(p []byte) (int, error) {
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

var _ = io.Reader(&assembler{})

// very simple implementation, mostly used for assembling narinfo which is
// usually tiny to avoid overhead of creating files.
func assemble(store desync.Store, index desync.Index) io.ReadCloser {
	return newAssembler(store, index)
}

func assembleNarinfo(store desync.Store, index desync.Index) (*Narinfo, error) {
	buf := assemble(store, index)

	info := &Narinfo{}
	err := info.Unmarshal(buf)
	if err != nil {
		return info, errors.WithMessage(err, "while unmarshaling narinfo")
	}

	return info, nil
}
