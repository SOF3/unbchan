package unbchan_test

import (
	"sync"
	"testing"

	"github.com/SOF3/unbchan"

	"github.com/stretchr/testify/assert"
)

func TestFifoSendAndRecv(t *testing.T) {
	ch := unbchan.New[uint32]()

	for i := range uint32(16) {
		ch.Send(i)
	}

	for expected := range uint32(16) {
		actual := ch.Recv()
		assert.Equal(t, expected, actual)
	}
}

func TestMpmcSendAndRecv(t *testing.T) {
	type item struct {
		gr uint8
		ord uint32
	}

	ch := unbchan.New[item]()

	output := make(chan item, 64*64)

	wg := sync.WaitGroup{}
	wg.Add(64)

	for gr := range uint8(64) {
		go func(){
			for ord := range uint32(64) {
				ch.Send(item{gr: gr, ord: ord})
			}
		}()
		go func(){
			for range 64 {
				output <- ch.Recv()
			}
			wg.Done()
		}()
	}

	wg.Wait()
	close(output)

	expectedNext := make([]uint32, 64)
	for item := range output {
		assert.Equal(t, expectedNext[item.gr], item.ord)
		expectedNext[item.gr]++
	}
}

func BenchmarkSendBurst(b *testing.B) {
	ch := unbchan.New[struct{}]()

	for range b.N {
		ch.Send(struct{}{})
	}
}
