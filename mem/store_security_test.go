package mem

import (
	"github.com/cloudwego/eino/schema"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionPathsCannotEscapeStore(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore[*schema.Message](dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../escape", "/absolute", "a/b", "a\\b", "", ".."} {
		if _, err := s.GetOrCreate(id); err == nil {
			t.Errorf("GetOrCreate accepted %q", id)
		}
		if err := s.Delete(id); err == nil {
			t.Errorf("Delete accepted %q", id)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.jsonl")); !os.IsNotExist(err) {
		t.Fatal("escaped store directory")
	}
}

func TestFailedAppendDoesNotCreatePhantomHistory(t *testing.T) {
	s, err := NewStore[*schema.Message](t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.GetOrCreate("test-append")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sess.filePath); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(schema.UserMessage("cannot persist")); err == nil {
		t.Fatal("append must fail")
	}
	if len(sess.GetMessages()) != 0 {
		t.Fatal("failed write leaked into history")
	}
}

func TestToolEvidenceSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore[*schema.Message](dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.GetOrCreate("large-evidence")
	if err != nil {
		t.Fatal(err)
	}
	text := string(make([]byte, 100000))
	if err := sess.Append(schema.ToolMessage(text, "call-1")); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore[*schema.Message](dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.GetOrCreate("large-evidence")
	if err != nil {
		t.Fatal(err)
	}
	messages := loaded.GetMessages()
	if len(messages) != 1 || messages[0].Content != text {
		t.Fatal("lost tool evidence after restart")
	}
}
