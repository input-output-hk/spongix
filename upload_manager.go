package main

import (
	"bytes"
	"time"

	"github.com/folbricht/desync"
)

type uploadManager struct {
	c chan uploadMsg
}

func newUploadManager(store desync.WriteStore, index desync.IndexWriteStore) uploadManager {
	return uploadManager{c: uploadLoop(store, index)}
}

func (m uploadManager) new(uuid string) {
	m.c <- uploadMsg{t: uploadMsgNew, uuid: uuid}
}

func (m uploadManager) get(uuid string) *dockerUpload {
	c := make(chan *dockerUpload)
	m.c <- uploadMsg{t: uploadMsgGet, uuid: uuid, c: c}
	return <-c
}

func (m uploadManager) del(uuid string) *dockerUpload {
	c := make(chan *dockerUpload)
	m.c <- uploadMsg{t: uploadMsgGet, uuid: uuid, c: c}
	return <-c
}

type uploadMsg struct {
	t    uploadMsgType
	c    chan *dockerUpload
	uuid string
}

type uploadMsgType int

const (
	uploadMsgNew uploadMsgType = iota
	uploadMsgGet uploadMsgType = iota
	uploadMsgDel uploadMsgType = iota
)

func uploadLoop(store desync.WriteStore, index desync.IndexWriteStore) chan uploadMsg {
	uploads := map[string]*dockerUpload{}

	ch := make(chan uploadMsg, 10)
	go func() {
		for msg := range ch {
			switch msg.t {
			case uploadMsgNew:
				uploads[msg.uuid] = &dockerUpload{
					uuid:         msg.uuid,
					content:      &bytes.Buffer{},
					lastModified: time.Now(),
				}
			case uploadMsgGet:
				upload, ok := uploads[msg.uuid]
				if ok {
					msg.c <- upload
				} else {
					msg.c <- nil
				}
			case uploadMsgDel:
				delete(uploads, msg.uuid)
				msg.c <- nil
			default:
				panic(msg)
			}
		}
	}()

	return ch
}
