package unbchan

import (
	"sync"
	"sync/atomic"
)

type Channel[T any] struct {
	activeGen atomic.Pointer[chGen[T]]
}

type chGen[T any] struct {
	// Indicates whether the previous allocation is flushed.
	initialFlushed <-chan struct{}
	// The actual buffered channel for sending data.
	dataCh chan T
	// Read is acquired when a goroutine has pending items to send into dataCh.
	// Write is acquired when a goroutine decided to reallocate this channel.
	sendLock sync.RWMutex
}

func New[T any]() *Channel[T] {
	return NewWithCapacity[T](1)
}

func NewWithCapacity[T any](initialCapacity int) *Channel[T] {
	noInitialData := make(chan struct{})
	close(noInitialData)

	dataCh := &Channel[T]{
		activeGen: atomic.Pointer[chGen[T]]{},
	}

	dataCh.activeGen.Store(&chGen[T]{
		initialFlushed: noInitialData,
		dataCh:         make(chan T, initialCapacity),
		sendLock:       sync.RWMutex{},
	})

	return dataCh
}

func (ch *Channel[T]) Recv() T {
	for {
		gen := ch.activeGen.Load()
		item, available := <-gen.dataCh
		if available {
			return item
		}

		// dataCh is closed, implying that activeGen has been updated
	}
}

func (ch *Channel[T]) operate(
	fastPath func(chan<- T) bool,
	lockedPath func(chan<- T),
	blocking bool,
) {
	for {
		oldGen := ch.activeGen.Load()

		<-oldGen.initialFlushed

		if !oldGen.sendLock.TryRLock() {
			// generation is fused by another goroutine
			continue
		}

		canExit := fastPath(oldGen.dataCh)

		oldGen.sendLock.RUnlock() // no need to hold the RLock anymore since we are not writing it anymore

		if canExit {
			return
		}

		// insufficient capacity, allocate a new one

		initialFlushed := make(chan struct{})
		dataCh := make(chan T, cap(oldGen.dataCh)*2)

		newGen := &chGen[T]{
			initialFlushed: initialFlushed,
			dataCh:         dataCh,
			sendLock:       sync.RWMutex{},
		}

		if !ch.activeGen.CompareAndSwap(oldGen, newGen) {
			// another goroutine reallocated the generation, reload activeGen
			continue
		}

		// Send is almost non-blocking, and there is no need to wait for the initial flush or the new item send before returning.
		finalize := func() {
			// Permanently acquires the sendLock.
			// This should not deadlock any goroutines in any case,
			// since we are the only goroutine who obsoleted oldGen (guaranteed by activeGen.CompareAndSwap).
			// This just tells other goroutines, in the case of a race condition, no longer to send to oldGen.
			oldGen.sendLock.Lock()

			// Since nobody else is sending to oldGen (as protected by sendLock),
			// we can safely close dataCh so that the following for loop will terminate.
			close(oldGen.dataCh)

			// Redirect all items from the old channel to this new one.
			for oldItem := range oldGen.dataCh {
				newGen.dataCh <- oldItem
			}

			// Now we can send the new item.
			lockedPath(newGen.dataCh)

			// The old data have finished sending, i.e. we have satisfied the FIFO requirement.
			// Note that FIFO also requires the reallocating Send call to precede other data,
			// because we are flushing from a different goroutine,
			// i.e. new data may be flushed from the caller goroutine and send into dataCh before this one
			// if we close initialFlushed first.
			close(initialFlushed)
		}

		if blocking {
			finalize()
		} else {
			go finalize()
		}

		return
	}
}

func (ch *Channel[T]) Send(item T) {
	ch.operate(
		func(dataCh chan<- T) bool {
		select {
		case dataCh <- item:
			return true
		default:
			return false
		}
		},
		func(dataCh chan<- T) {
			dataCh <- item
		},
		false,
	)
}

func (ch *Channel[T]) Close() {
	ch.operate(
		func(_ chan<- T) bool { return false },
		func(dataCh chan<- T) { close(dataCh) },
		true,
	)
}
