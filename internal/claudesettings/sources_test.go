package claudesettings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSource(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func requireSourceUnchanged(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("destination changed after failed preflight: %s: %q, %v", path, got, err)
	}
}

func TestPreparedImportRejectsChangedSources(t *testing.T) {
	for _, source := range []string{"shared", "account"} {
		for _, change := range []string{"edit", "create", "remove"} {
			t.Run(source+"/"+change, func(t *testing.T) {
				dir := t.TempDir()
				shared, account, destination := filepath.Join(dir, "shared"), filepath.Join(dir, "account"), filepath.Join(dir, "destination")
				changed := shared
				if source == "account" {
					changed = account
				}
				for path, body := range map[string]string{
					shared:  `{"permissions":{"allow":["Bash(*)"]},"autoMode":{"allow":["old rule"]}}`,
					account: `{"apiKeyHelper":"synthetic-account-helper"}`,
				} {
					if path != changed || change != "create" {
						writeSource(t, path, body)
					}
				}
				const before = `{"apiKeyHelper":"synthetic-previous-helper","model":"private"}`
				writeSource(t, destination, before)
				u, err := PrepareImport(shared, account, destination, Policy{})
				if err != nil {
					t.Fatal(err)
				}
				if change == "remove" {
					if err := os.Rename(changed, changed+".saved"); err != nil {
						t.Fatal(err)
					}
				} else {
					writeSource(t, changed, `{"permissions":{"allow":[]},"apiKeyHelper":"synthetic-new-helper"}`)
				}
				if err := u.Apply(); err == nil || !strings.Contains(err.Error(), "changed during activation") {
					t.Fatalf("source %s accepted after %s: %v", source, change, err)
				}
				requireSourceUnchanged(t, destination, before)
			})
		}
	}
}

func TestPreparedRestoreRejectsRotatedAccountSettings(t *testing.T) {
	dir := t.TempDir()
	live, snapshot := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot")
	const before = `{"permissions":{"allow":["Read"]},"apiKeyHelper":"synthetic-alice"}`
	writeSource(t, live, before)
	writeSource(t, snapshot, `{"env":{"ANTHROPIC_API_KEY":"synthetic-bob-old"}}`)
	u, err := PrepareRestore(snapshot, live, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	writeSource(t, snapshot, `{"env":{"ANTHROPIC_API_KEY":"synthetic-bob-new"}}`)
	if err := u.Apply(); err == nil {
		t.Fatal("activated settings from a snapshot that rotated during preparation")
	}
	requireSourceUnchanged(t, live, before)
}

func TestBatchChecksEverySourceBeforeAnyDestinationWrite(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared")
	writeSource(t, shared, `{"model":"new-policy"}`)
	var updates []*Update
	for _, name := range []string{"one", "two"} {
		account, dest := filepath.Join(dir, name+"-account"), filepath.Join(dir, name)
		writeSource(t, account, `{"apiKeyHelper":"synthetic-`+name+`"}`)
		writeSource(t, dest, `{"model":"unchanged"}`)
		u, err := PrepareImport(shared, account, dest, Policy{})
		if err != nil {
			t.Fatal(err)
		}
		updates = append(updates, u)
	}
	writeSource(t, filepath.Join(dir, "two-account"), `{"apiKeyHelper":"synthetic-rotated"}`)
	if err := ApplyUpdates(updates); err == nil {
		t.Fatal("batch ignored a changed source in its second update")
	}
	for _, name := range []string{"one", "two"} {
		requireSourceUnchanged(t, filepath.Join(dir, name), `{"model":"unchanged"}`)
	}
}

func TestIsolatedRefreshChecksPolicySourceEvenForDirectApply(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "batch"}[batch], func(t *testing.T) {
			dir := t.TempDir()
			shared := filepath.Join(dir, "shared")
			writeSource(t, shared, `{"permissions":{"allow":["Bash(*)"]},"hooks":{"Stop":[]}}`)
			paths := []string{filepath.Join(dir, "legacy", "settings.json"), filepath.Join(dir, "xdg", "settings.json")}
			const before = `{"apiKeyHelper":"synthetic-private","model":"before"}`
			for _, path := range paths {
				writeSource(t, path, before)
			}
			updates, err := PrepareIsolatedSettings(shared, paths, Policy{})
			if err != nil {
				t.Fatal(err)
			}
			writeSource(t, shared, `{"permissions":{"allow":[]}}`)
			if batch {
				err = ApplyUpdates(updates)
			} else {
				err = updates[0].Apply()
			}
			if err == nil {
				t.Fatal("revoked policy could still be written into isolated profile")
			}
			for _, path := range paths {
				requireSourceUnchanged(t, path, before)
			}
		})
	}
}

func TestNoopRefreshStillChecksSource(t *testing.T) {
	dir := t.TempDir()
	shared, dest := filepath.Join(dir, "shared"), filepath.Join(dir, "dest")
	const before = "{\n  \"model\": \"same\"\n}\n"
	writeSource(t, shared, before)
	writeSource(t, dest, before)
	u, err := PrepareRefresh(shared, dest, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if u.Changed() {
		t.Fatal("fixture must exercise a no-op refresh")
	}
	writeSource(t, shared, `{"model":"edited"}`)
	if err := ApplyUpdates([]*Update{u}); err == nil {
		t.Fatal("unchanged destination hid an edited source")
	}
	requireSourceUnchanged(t, dest, before)
}

func TestStableIsolatedBatchPreservesEachAccountsAuthentication(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared")
	const policy = `{"permissions":{"allow":["Read"],"defaultMode":"auto"},"autoMode":{"allow":["test"]},"mcpServers":{"new":{}},"hooks":{"Stop":[]},"apiKeyHelper":"synthetic-host"}`
	writeSource(t, shared, policy)
	paths := []string{filepath.Join(dir, "alice", "settings.json"), filepath.Join(dir, "bob", "settings.json"), filepath.Join(dir, "oauth", "settings.json")}
	for i, body := range []string{`{"apiKeyHelper":"synthetic-alice"}`, `{"env":{"ANTHROPIC_API_KEY":"synthetic-bob"}}`, `{}`} {
		writeSource(t, paths[i], body)
	}
	updates, err := PrepareIsolatedSettings(shared, paths, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUpdates(updates); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{`{"apiKeyHelper":"synthetic-alice"}`, `{"env":{"ANTHROPIC_API_KEY":"synthetic-bob"}}`, `{}`} {
		data, err := os.ReadFile(paths[i])
		if err != nil {
			t.Fatal(err)
		}
		id, err := Identity(data, Policy{})
		if err != nil || string(id) != want {
			t.Fatalf("account %d authentication changed: %s, %v", i, id, err)
		}
		obj, err := object(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"permissions", "autoMode", "mcpServers", "hooks"} {
			if _, ok := obj[key]; !ok {
				t.Fatalf("account %d lost shared policy %s", i, key)
			}
		}
	}
	requireSourceUnchanged(t, shared, policy)
}

func TestInputSnapshotsReadEachCanonicalPathOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	const before = `{"apiKeyHelper":"synthetic-original"}`
	writeSource(t, path, before)
	inputs := make(settingsInputs)
	first, err := inputs.read(path)
	if err != nil {
		t.Fatal(err)
	}
	writeSource(t, path, `{"apiKeyHelper":"synthetic-edited"}`)
	alias := dir + string(os.PathSeparator) + "." + string(os.PathSeparator) + "settings.json"
	second, err := inputs.read(alias)
	if err != nil || string(first) != before || string(second) != before || len(inputs) != 1 {
		t.Fatalf("same input was read twice: %q, %q, %v", first, second, err)
	}
	u, err := preparedUpdate(path, first, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	u.inputs = inputs
	if err := u.Apply(); err == nil {
		t.Fatal("paired an old account read with a newer destination snapshot")
	}
	requireSourceUnchanged(t, path, `{"apiKeyHelper":"synthetic-edited"}`)
}
