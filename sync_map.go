package roq

import (
	"maps"
	"sync"
)

type syncMap[K comparable, V any] struct {
	mutex    sync.Mutex
	elements map[K]V
}

func newSyncMap[K comparable, V any]() *syncMap[K, V] {
	return &syncMap[K, V]{
		mutex:    sync.Mutex{},
		elements: make(map[K]V),
	}
}

// getOrInsert returns the value stored under k, inserting v if the map holds
// no entry for k yet. The second return value reports whether v was inserted.
func (m *syncMap[K, V]) getOrInsert(k K, v V) (V, bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if old, ok := m.elements[k]; ok {
		return old, false
	}
	m.elements[k] = v
	return v, true
}

// set stores v under k, replacing any entry the map holds for k.
func (m *syncMap[K, V]) set(k K, v V) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.elements[k] = v
}

func (m *syncMap[K, V]) get(k K) (V, bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	v, ok := m.elements[k]
	return v, ok
}

func (m *syncMap[K, V]) delete(k K) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	delete(m.elements, k)
}

// rangeFn calls f for every element in the map. f is called without the map
// lock held, so it may add to or delete from the map. f therefore observes a
// snapshot taken at the time of the call, not the live map.
func (m *syncMap[K, V]) rangeFn(f func(k K, v V)) {
	m.mutex.Lock()
	elements := maps.Clone(m.elements)
	m.mutex.Unlock()
	for k, v := range elements {
		f(k, v)
	}
}
