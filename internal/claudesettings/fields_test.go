package claudesettings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFieldsRestoreRetiresOnlySelectedAccountFields(t *testing.T) {
	for _, source := range []string{"absent", "policy-only", "different-cache"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			live, snapshot := filepath.Join(dir, "live.json"), filepath.Join(dir, "snapshot.json")
			const before = `{"theme":"live","env":"unrelated-schema","futureCounter":9007199254740993,"oauth:tokenCache":"old-v1","oauth:tokenCacheV2":"old-v2"}`
			batchTestWrite(t, live, before)
			if source == "policy-only" {
				batchTestWrite(t, snapshot, `{"theme":"stale"}`)
			} else if source == "different-cache" {
				batchTestWrite(t, snapshot, `{"theme":"stale","oauth:tokenCacheV2":"incoming"}`)
			}
			update, err := PrepareFieldsRestore(snapshot, live, []string{"oauth:tokenCache", "oauth:tokenCacheV2"})
			if err != nil {
				t.Fatal(err)
			}
			if got, err := Read(live); err != nil || string(got) != before {
				t.Fatalf("preflight wrote the live document: %s, %v", got, err)
			}
			if err := update.Apply(); err != nil {
				t.Fatal(err)
			}
			got, err := Read(live)
			if err != nil {
				t.Fatal(err)
			}
			var want string
			if source == "different-cache" {
				want = "{\n  \"env\": \"unrelated-schema\",\n  \"futureCounter\": 9007199254740993,\n  \"oauth:tokenCacheV2\": \"incoming\",\n  \"theme\": \"live\"\n}\n"
			} else {
				want = "{\n  \"env\": \"unrelated-schema\",\n  \"futureCounter\": 9007199254740993,\n  \"theme\": \"live\"\n}\n"
			}
			if string(got) != want {
				t.Fatalf("cache restore changed unrelated policy or retained outgoing auth: %s", got)
			}
		})
	}
}

func TestFieldsRestorePreflightAndSourceGuards(t *testing.T) {
	dir := t.TempDir()
	live, snapshot := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot")
	keys := []string{"oauth:tokenCacheV2"}
	batchTestWrite(t, live, `{"theme":"live","oauth:tokenCacheV2":"old"}`)
	before, _ := Read(live)
	update, err := PrepareFieldsRestore(snapshot, live, keys)
	if err != nil {
		t.Fatal(err)
	}
	batchTestWrite(t, snapshot, `{"oauth:tokenCacheV2":"new"}`)
	if err := ApplyUpdates([]*Update{update}); err == nil {
		t.Fatal("accepted snapshot creation after preflight")
	}
	if got, err := Read(live); err != nil || string(got) != string(before) {
		t.Fatalf("source conflict changed live document: %s, %v", got, err)
	}
	batchTestWrite(t, snapshot, `[]`)
	if _, err := PrepareFieldsRestore(snapshot, live, keys); err == nil {
		t.Fatal("accepted malformed snapshot")
	}
	if _, err := PrepareFieldsRestore(snapshot, live, []string{""}); err == nil {
		t.Fatal("accepted empty account field")
	}
	absent := filepath.Join(dir, "absent")
	update, err = PrepareFieldsRestore(filepath.Join(dir, "missing"), absent, keys)
	if err != nil {
		t.Fatal(err)
	}
	if err := update.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatalf("absent config was spuriously created: %v", err)
	}
}
