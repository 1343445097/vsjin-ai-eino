package webui

import (
	"bytes"
	"testing"
)

func TestIndexIsEmbedded(t *testing.T) {
	if len(Index) == 0 {
		t.Fatal("embedded index is empty")
	}
	for _, want := range [][]byte{
		[]byte("<!doctype html>"),
		[]byte("<html lang=\"zh-CN\">"),
		[]byte("VSJin AI"),
	} {
		if !bytes.Contains(Index, want) {
			t.Errorf("embedded index does not contain %q", want)
		}
	}
}
