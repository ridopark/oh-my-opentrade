package ibkr

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

func TestOnReconnect_SingleCallback(t *testing.T) {
	conn := &connection{log: zerolog.Nop()}
	var called atomic.Bool
	conn.OnReconnect(func() { called.Store(true) })
	conn.fireReconnectCallbacks()
	time.Sleep(50 * time.Millisecond)
	assert.True(t, called.Load())
}

func TestOnReconnect_MultipleCallbacks(t *testing.T) {
	conn := &connection{log: zerolog.Nop()}
	var count atomic.Int32
	conn.OnReconnect(func() { count.Add(1) })
	conn.OnReconnect(func() { count.Add(1) })
	conn.OnReconnect(func() { count.Add(1) })
	conn.fireReconnectCallbacks()
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(3), count.Load())
}

func TestOnReconnect_NoCallbacks_NoPanic(t *testing.T) {
	conn := &connection{log: zerolog.Nop()}
	assert.NotPanics(t, func() { conn.fireReconnectCallbacks() })
}

func TestOnReconnect_ConcurrentRegistration(t *testing.T) {
	conn := &connection{log: zerolog.Nop()}
	var count atomic.Int32
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			conn.OnReconnect(func() { count.Add(1) })
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	conn.fireReconnectCallbacks()
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(10), count.Load())
}
