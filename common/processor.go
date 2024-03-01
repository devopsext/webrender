package common

import (
	"net/http"
	"reflect"
)

type Processor interface {
	Type() string
}

type HttpProcessor interface {
	Processor
	HandleHttpRequest(w http.ResponseWriter, r *http.Request) error
}

type Processors struct {
	list []Processor
}

func (ps *Processors) Add(p Processor) {

	if reflect.ValueOf(p).IsNil() {
		return
	}
	ps.list = append(ps.list, p)
}

func (ps *Processors) Find(typ string) Processor {
	for _, p := range ps.list {
		if p.Type() == typ {
			return p
		}
	}
	return nil
}

func (ps *Processors) FindHttpProcessor(typ string) HttpProcessor {
	for _, p := range ps.list {
		hp, ok := p.(HttpProcessor)
		if ok && hp.Type() == typ {
			return hp
		}
	}
	return nil
}

func NewProcessors() *Processors {
	return &Processors{}
}
