package common

import (
	"reflect"
	"sync"
)

type Server interface {
	Start(wg *sync.WaitGroup)
}

type Servers struct {
	list []Server
}

func (ss *Servers) Add(s Server) {

	if reflect.ValueOf(s).IsNil() {
		return
	}
	ss.list = append(ss.list, s)
}

func (ss *Servers) Start(wg *sync.WaitGroup) {

	for _, i := range ss.list {

		if i != nil {
			(i).Start(wg)
		}
	}
}

func NewServers() Servers {
	return Servers{}
}
