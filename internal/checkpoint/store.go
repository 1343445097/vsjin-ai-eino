/*
 * Copyright 2025 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package checkpoint

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/compose"
)

func NewInMemoryStore() compose.CheckPointStore {
	return &inMemoryStore{
		mem: map[string][]byte{},
	}
}

type inMemoryStore struct {
	mu  sync.RWMutex
	mem map[string][]byte
}

func (i *inMemoryStore) Set(ctx context.Context, key string, value []byte) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.mem[key] = append([]byte(nil), value...)
	return nil
}

func (i *inMemoryStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	v, ok := i.mem[key]
	return append([]byte(nil), v...), ok, nil
}

func (i *inMemoryStore) Delete(_ context.Context, key string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.mem, key)
	return nil
}
