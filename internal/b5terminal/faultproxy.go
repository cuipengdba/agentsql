package b5terminal

import (
	"sort"
	"sync"
)

type FaultEventKind uint8

const (
	FaultEventCancelPacket FaultEventKind = iota
	FaultEventMainCommand
	FaultEventExecuteSuccess
	FaultEventReadyForQueryIdle
	FaultEventCancelSocketClosed
	FaultEventStatementTimeout
)

type FaultEvent struct {
	Kind     FaultEventKind
	Command  MainCommand
	At       uint64
	Sequence uint64
}

// FaultProxy is a deterministic, in-memory two-path proxy. Delay controls when
// the server observes an event; CancelGuard is updated when the client send
// starts, which models the dangerous interval before a delayed cancel arrives.
type FaultProxy struct {
	mu        sync.Mutex
	now       uint64
	next      uint64
	guard     *CancelGuard
	pending   []FaultEvent
	delivered []FaultEvent
}

func NewFaultProxy(guard *CancelGuard) *FaultProxy {
	if guard == nil {
		guard = NewCancelGuard()
	}
	return &FaultProxy{guard: guard}
}

func (proxy *FaultProxy) SendCancel(emission CancelEmission, delay uint64) {
	proxy.guard.ObserveEmission(emission)
	if emission == CancelNotSentProven {
		return
	}
	proxy.schedule(FaultEvent{Kind: FaultEventCancelPacket}, delay)
}

func (proxy *FaultProxy) SendMain(command MainCommand, delay uint64) error {
	if err := proxy.guard.RecordMainSend(command); err != nil {
		return err
	}
	proxy.schedule(FaultEvent{Kind: FaultEventMainCommand, Command: command}, delay)
	return nil
}

func (proxy *FaultProxy) ScheduleServerEvent(kind FaultEventKind, delay uint64) {
	proxy.schedule(FaultEvent{Kind: kind}, delay)
}

func (proxy *FaultProxy) schedule(event FaultEvent, delay uint64) {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	proxy.next++
	event.At = proxy.now + delay
	event.Sequence = proxy.next
	proxy.pending = append(proxy.pending, event)
}

func (proxy *FaultProxy) AdvanceTo(at uint64) []FaultEvent {
	proxy.mu.Lock()
	if at < proxy.now {
		proxy.mu.Unlock()
		return nil
	}
	proxy.now = at
	sort.SliceStable(proxy.pending, func(i, j int) bool {
		if proxy.pending[i].At == proxy.pending[j].At {
			return proxy.pending[i].Sequence < proxy.pending[j].Sequence
		}
		return proxy.pending[i].At < proxy.pending[j].At
	})
	ready := make([]FaultEvent, 0)
	rest := proxy.pending[:0]
	for _, event := range proxy.pending {
		if event.At <= at {
			ready = append(ready, event)
		} else {
			rest = append(rest, event)
		}
	}
	proxy.pending = rest
	proxy.delivered = append(proxy.delivered, ready...)
	proxy.mu.Unlock()

	for _, event := range ready {
		switch event.Kind {
		case FaultEventExecuteSuccess:
			proxy.guard.ObserveExecuteExit()
		case FaultEventReadyForQueryIdle:
			proxy.guard.ObserveReadyForQueryIdle()
		case FaultEventCancelSocketClosed:
			proxy.guard.ObserveCancelSocketClosed()
		case FaultEventStatementTimeout:
			proxy.guard.ObserveStatementTimeout()
		}
	}
	return ready
}

func (proxy *FaultProxy) Delivered() []FaultEvent {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	result := make([]FaultEvent, len(proxy.delivered))
	copy(result, proxy.delivered)
	return result
}

func (proxy *FaultProxy) Pending() []FaultEvent {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	result := make([]FaultEvent, len(proxy.pending))
	copy(result, proxy.pending)
	return result
}
