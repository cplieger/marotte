package agent

import "sync"

var pumpBufPool = sync.Pool{
	New: func() any { return make([]byte, 4096) },
}

// getPumpBuf returns a pooled 4 KB buffer, or a fresh one should the pool yield another type.
func getPumpBuf() []byte {
	buf, ok := pumpBufPool.Get().([]byte)
	if !ok {
		return make([]byte, 4096)
	}
	return buf
}
