package main

import (
	"bytes"
	"context"

	"github.com/folbricht/desync"
	"github.com/pkg/errors"
)

type blobManager struct {
	c     chan blobMsg
	store desync.WriteStore
	index desync.IndexWriteStore
}

func newBlobManager(store desync.WriteStore, index desync.IndexWriteStore) blobManager {
	c := make(chan blobMsg, 10)
	manager := blobManager{c: c, store: store, index: index}
	manager.loop()
	return manager
}

func (m blobManager) get(name, digest string) ([]byte, error) {
	c := make(chan blobResponse)
	m.c <- blobMsg{t: blobMsgGet, name: name, digest: digest, c: c}
	msg := <-c
	return msg.blob, msg.err
}

func (m blobManager) set(name, digest string, blob []byte) error {
	c := make(chan blobResponse)
	m.c <- blobMsg{t: blobMsgSet, name: name, digest: digest, blob: blob, c: c}
	msg := <-c
	return msg.err
}

// used to communicate with the blob registry
type blobMsg struct {
	t      blobMsgType
	name   string
	digest string
	blob   []byte
	c      chan blobResponse
}

func (m blobMsg) Key() string {
	return m.name + "'" + m.digest
}

type blobResponse struct {
	blob []byte
	err  error
}

type blobMsgType int

const (
	blobMsgSet blobMsgType = iota
	blobMsgGet blobMsgType = iota
)

func (m blobManager) loop() {
	blobSet := func(msg blobMsg) error {
		if chunker, err := desync.NewChunker(bytes.NewBuffer(msg.blob), chunkSizeMin(), chunkSizeAvg, chunkSizeMax()); err != nil {
			return errors.WithMessage(err, "making chunker")
		} else if idx, err := desync.ChunkStream(context.Background(), chunker, m.store, defaultThreads); err != nil {
			return errors.WithMessage(err, "chunking blob")
		} else if err := m.index.StoreIndex(msg.Key(), idx); err != nil {
			return errors.WithMessage(err, "storing index")
		}

		return nil
	}

	blobGet := func(msg blobMsg) ([]byte, error) {
		if idx, err := m.index.GetIndex(msg.Key()); err != nil {
			return nil, errors.WithMessage(err, "getting index")
		} else {
			buf := &bytes.Buffer{}

			for _, indexChunk := range idx.Chunks {
				if chunk, err := m.store.GetChunk(indexChunk.ID); err != nil {
					return nil, errors.WithMessage(err, "getting chunk for index")
				} else if data, err := chunk.Data(); err != nil {
					return nil, errors.WithMessage(err, "getting chunk data")
				} else if _, err := buf.Write(data); err != nil {
					return nil, errors.WithMessage(err, "writing chunk data")
				}
			}

			return buf.Bytes(), nil
		}
	}

	for msg := range m.c {
		switch msg.t {
		case blobMsgSet:
			if err := blobSet(msg); err != nil {
				msg.c <- blobResponse{err: err}
			} else {
				msg.c <- blobResponse{}
			}
		case blobMsgGet:
			if blob, err := blobGet(msg); err != nil {
				msg.c <- blobResponse{err: err}
			} else {
				msg.c <- blobResponse{blob: blob}
			}
		default:
			panic(msg)
		}
	}
}
