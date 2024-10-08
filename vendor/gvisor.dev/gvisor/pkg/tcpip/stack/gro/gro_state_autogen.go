// automatically generated by stateify.

package gro

import (
	"context"

	"gvisor.dev/gvisor/pkg/state"
)

func (l *groPacketList) StateTypeName() string {
	return "pkg/tcpip/stack/gro.groPacketList"
}

func (l *groPacketList) StateFields() []string {
	return []string{
		"head",
		"tail",
	}
}

func (l *groPacketList) beforeSave() {}

// +checklocksignore
func (l *groPacketList) StateSave(stateSinkObject state.Sink) {
	l.beforeSave()
	stateSinkObject.Save(0, &l.head)
	stateSinkObject.Save(1, &l.tail)
}

func (l *groPacketList) afterLoad(context.Context) {}

// +checklocksignore
func (l *groPacketList) StateLoad(ctx context.Context, stateSourceObject state.Source) {
	stateSourceObject.Load(0, &l.head)
	stateSourceObject.Load(1, &l.tail)
}

func (e *groPacketEntry) StateTypeName() string {
	return "pkg/tcpip/stack/gro.groPacketEntry"
}

func (e *groPacketEntry) StateFields() []string {
	return []string{
		"next",
		"prev",
	}
}

func (e *groPacketEntry) beforeSave() {}

// +checklocksignore
func (e *groPacketEntry) StateSave(stateSinkObject state.Sink) {
	e.beforeSave()
	stateSinkObject.Save(0, &e.next)
	stateSinkObject.Save(1, &e.prev)
}

func (e *groPacketEntry) afterLoad(context.Context) {}

// +checklocksignore
func (e *groPacketEntry) StateLoad(ctx context.Context, stateSourceObject state.Source) {
	stateSourceObject.Load(0, &e.next)
	stateSourceObject.Load(1, &e.prev)
}

func init() {
	state.Register((*groPacketList)(nil))
	state.Register((*groPacketEntry)(nil))
}
