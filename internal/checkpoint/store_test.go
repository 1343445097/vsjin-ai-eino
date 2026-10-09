package checkpoint

import (
	"context"
	"sync"
	"testing"
)

func TestCheckpointCopiesAndSupportsConcurrentTurns(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	data := []byte("checkpoint")
	if err := s.Set(ctx, "run", data); err != nil {
		t.Fatal(err)
	}
	data[0] = 'X'
	got, ok, err := s.Get(ctx, "run")
	if err != nil || !ok || string(got) != "checkpoint" {
		t.Fatalf("checkpoint must own bytes: %q", got)
	}
	got[0] = 'Y'
	again, _, _ := s.Get(ctx, "run")
	if string(again) != "checkpoint" {
		t.Fatal("read leaked mutable bytes")
	}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Set(ctx, "parallel", []byte("state")); _, _, _ = s.Get(ctx, "parallel") }()
	}
	wg.Wait()
}
