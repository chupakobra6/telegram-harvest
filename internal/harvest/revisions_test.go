package harvest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRevisionRebuildsViewsWithOnlyLatestText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "messages.jsonl")
	encoder, file, err := OpenJSONL(path, false)
	if err != nil {
		t.Fatal(err)
	}
	old := record(11, nil)
	old.Text = "obsolete assignment"
	if err := encoder.Encode(old); err != nil {
		t.Fatal(err)
	}
	file.Close()
	opts := AgentViewOptions{InputPath: path, OutputDir: filepath.Join(dir, "view")}
	if _, err := UpdateAgentMarkdownView(opts); err != nil {
		t.Fatal(err)
	}
	encoder, file, err = OpenJSONL(path, true)
	if err != nil {
		t.Fatal(err)
	}
	revised := old
	revised.Text = "corrected deadline"
	revised.Revision = true
	if err := encoder.Encode(revised); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := UpdateAgentMarkdownView(opts); err != nil {
		t.Fatal(err)
	}
	found := false
	err = filepath.WalkDir(opts.OutputDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), old.Text) {
			t.Errorf("old revision in %s", path)
		}
		found = found || strings.Contains(string(data), revised.Text)
		return nil
	})
	if err != nil || !found {
		t.Fatalf("view err=%v found=%t", err, found)
	}
	compact, _, err := readCompactRecords(CompactOptions{InputPath: path})
	if err != nil || len(compact) != 1 || compact[0].Text != revised.Text {
		t.Fatalf("compact=%+v err=%v", compact, err)
	}
}
