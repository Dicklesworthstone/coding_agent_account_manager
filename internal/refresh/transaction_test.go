package refresh

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	profilepkg "github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

type transactionFixture struct {
	vault              *authfile.Vault
	health             *health.Storage
	profiles           *profilepkg.Store
	source, live, home string
	original           []byte
}

func newTransactionFixture(t *testing.T, provider string) transactionFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CAAM_HOME", root)
	t.Setenv("CODEX_HOME", filepath.Join(root, "live-codex"))
	t.Setenv("GEMINI_HOME", filepath.Join(root, "live-gemini"))
	f := transactionFixture{
		vault:    authfile.NewVault(filepath.Join(root, "vault")),
		health:   health.NewStorage(filepath.Join(root, "health.json")),
		profiles: profilepkg.NewStore(filepath.Join(root, "custom-profiles")),
	}
	prof, err := f.profiles.Create(provider, "work", "oauth")
	if err != nil {
		t.Fatal(err)
	}
	file := "auth.json"
	f.live = filepath.Join(os.Getenv("CODEX_HOME"), file)
	f.home = filepath.Join(prof.CodexHomePath(), file)
	body := map[string]any{
		"access_token": "SYNTHETIC-OLD-ACCESS", "refresh_token": "SYNTHETIC-OLD-REFRESH",
		"expires_at": time.Now().Add(time.Minute).Unix(), "account_id": "synthetic-account",
	}
	if provider == "gemini" {
		file = "oauth_creds.json"
		body["client_id"], body["client_secret"] = "SYNTHETIC-ID", "SYNTHETIC-SECRET"
		f.live = filepath.Join(os.Getenv("GEMINI_HOME"), file)
		f.home = filepath.Join(prof.HomePath(), ".gemini", file)
	}
	f.source = filepath.Join(f.vault.ProfilePath(provider, "work"), file)
	f.original, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.source, f.live, f.home} {
		writeTransactionFile(t, path, f.original)
	}
	return f
}

func writeTransactionFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func transactionServer(t *testing.T, provider string, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	if provider == "codex" {
		before := CodexTokenURL
		CodexTokenURL = server.URL
		t.Cleanup(func() { CodexTokenURL = before })
	} else {
		before := GeminiTokenURL
		GeminiTokenURL = server.URL
		t.Cleanup(func() { GeminiTokenURL = before })
	}
}

func transactionResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"access_token":"SYNTHETIC-NEW-ACCESS","refresh_token":"SYNTHETIC-NEW-REFRESH","expires_in":3600}`))
}

func awaitTransaction(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not reach the synthetic token endpoint")
	}
}

func selectTransactionAPIKey(t *testing.T, provider, path string) map[string][]byte {
	t.Helper()
	writes := make(map[string][]byte)
	if provider == "codex" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var auth map[string]any
		if err := json.Unmarshal(data, &auth); err != nil {
			t.Fatal(err)
		}
		auth["auth_mode"], auth["OPENAI_API_KEY"] = "apikey", "SYNTHETIC-SELECTED-KEY"
		data, err = json.Marshal(auth)
		if err != nil {
			t.Fatal(err)
		}
		writes[path] = data
	} else {
		writes[filepath.Join(filepath.Dir(path), "settings.json")] = []byte(`{"security":{"auth":{"selectedType":"gemini-api-key"}}}`)
		writes[filepath.Join(filepath.Dir(path), ".env")] = []byte("GEMINI_API_KEY=SYNTHETIC-SELECTED-KEY\n")
	}
	for path, data := range writes {
		writeTransactionFile(t, path, data)
	}
	return writes
}

func TestRefreshSelectedAPIKeyNeverExchangesUnusedOAuth(t *testing.T) {
	for _, provider := range []string{"codex", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			f := newTransactionFixture(t, provider)
			before := selectTransactionAPIKey(t, provider, f.source)
			for _, path := range []string{f.source, f.live, f.home} {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				before[path] = data
			}
			var requests atomic.Int32
			transactionServer(t, provider, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				transactionResponse(w)
			})
			if err := Preflight(provider, "work", f.vault); !IsSkipped(err) {
				t.Fatalf("selected key preflight = %v, want safe skip", err)
			}
			if err := RefreshProfile(context.Background(), provider, "work", f.vault, f.health, WithProfileStore(f.profiles)); !IsSkipped(err) {
				t.Fatalf("selected key refresh = %v, want safe skip", err)
			}
			if requests.Load() != 0 {
				t.Fatal("selected API key exchanged an unused OAuth refresh token")
			}
			for path, want := range before {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatal("skipped refresh modified credentials or selected mode")
				}
			}
			stored, err := f.health.Load()
			if err != nil || stored.Profiles[provider+"/work"] != nil {
				t.Fatalf("unused OAuth created provider verification: %+v, %v", stored, err)
			}
		})
	}
}

func TestRefreshDiscardsOutcomeWhenSelectedMethodChanges(t *testing.T) {
	for _, provider := range []string{"codex", "gemini"} {
		for _, outcome := range []string{"success", "rejection"} {
			t.Run(provider+"/"+outcome, func(t *testing.T) {
				f := newTransactionFixture(t, provider)
				entered, release := make(chan struct{}), make(chan struct{})
				transactionServer(t, provider, func(w http.ResponseWriter, r *http.Request) {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					if outcome == "rejection" {
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
						return
					}
					transactionResponse(w)
				})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result := make(chan error, 1)
				go func() {
					result <- RefreshProfile(ctx, provider, "work", f.vault, f.health, WithProfileStore(f.profiles))
				}()
				awaitTransaction(t, entered)
				before := selectTransactionAPIKey(t, provider, f.source)
				for _, path := range []string{f.source, f.live, f.home} {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					before[path] = data
				}
				close(release)
				if err := <-result; !errors.Is(err, ErrCredentialChanged) || !IsSkipped(err) {
					t.Fatalf("obsolete auth method result = %v, want changed-credential skip", err)
				}
				for path, want := range before {
					got, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(got, want) {
						t.Fatal("obsolete refresh outcome modified the selected credential or cache")
					}
				}
				stored, err := f.health.Load()
				if err != nil || stored.Profiles[provider+"/work"] != nil {
					t.Fatalf("obsolete auth method recorded a provider verdict: %+v, %v", stored, err)
				}
			})
		}
	}
}

func TestRefreshDeliveryRespectsSelectedMethod(t *testing.T) {
	for _, provider := range []string{"codex", "gemini"} {
		for _, destination := range []string{"live", "isolated"} {
			for _, during := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/during=%t", provider, destination, during), func(t *testing.T) {
					f := newTransactionFixture(t, provider)
					target := f.live
					if destination == "isolated" {
						target = f.home
					}
					var selected map[string][]byte
					if !during {
						selected = selectTransactionAPIKey(t, provider, target)
					}
					entered, release := make(chan struct{}), make(chan struct{})
					transactionServer(t, provider, func(w http.ResponseWriter, r *http.Request) {
						close(entered)
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
						transactionResponse(w)
					})
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					result := make(chan error, 1)
					go func() {
						result <- RefreshProfile(ctx, provider, "work", f.vault, f.health, WithProfileStore(f.profiles))
					}()
					awaitTransaction(t, entered)
					if during {
						selected = selectTransactionAPIKey(t, provider, target)
					}
					before, err := os.ReadFile(target)
					if err != nil {
						t.Fatal(err)
					}
					selected[target] = before
					close(release)
					err = <-result
					if IsDeliveryIncomplete(err) != during || (err != nil && !during) || IsSkipped(err) {
						t.Fatalf("delivery result = %v, want partial=%t", err, during)
					}
					for path, want := range selected {
						got, err := os.ReadFile(path)
						if err != nil || !bytes.Equal(got, want) {
							t.Fatal("OAuth delivery modified an API-key destination")
						}
					}
					for _, path := range []string{f.source, f.live, f.home} {
						if path == target {
							continue
						}
						got, err := os.ReadFile(path)
						if err != nil || !bytes.Contains(got, []byte("SYNTHETIC-NEW-ACCESS")) {
							t.Fatal("API destination prevented renewal of an unchanged OAuth owner")
						}
					}
				})
			}
		}
	}
}

func TestRefreshGeminiAllowsUnrelatedSettingsChange(t *testing.T) {
	f := newTransactionFixture(t, "gemini")
	settings := filepath.Join(filepath.Dir(f.source), "settings.json")
	writeTransactionFile(t, settings, []byte(`{"selectedAuthType":"oauth-personal","theme":"light"}`))
	transactionServer(t, "gemini", func(w http.ResponseWriter, r *http.Request) {
		if err := os.WriteFile(settings, []byte(`{"selectedAuthType":"oauth-personal","theme":"dark"}`), 0600); err != nil {
			t.Errorf("write synthetic settings: %v", err)
		}
		transactionResponse(w)
	})
	if err := RefreshProfile(context.Background(), "gemini", "work", f.vault, f.health, WithProfileStore(f.profiles)); err != nil {
		t.Fatalf("unrelated settings churn blocked OAuth renewal: %v", err)
	}
	got, err := os.ReadFile(settings)
	if err != nil || string(got) != `{"selectedAuthType":"oauth-personal","theme":"dark"}` {
		t.Fatal("OAuth renewal replaced native settings")
	}
}

func TestRefreshRejectsSourceReplacementDuringExchange(t *testing.T) {
	for _, provider := range []string{"codex", "gemini"} {
		for _, outcome := range []string{"success", "rejection"} {
			t.Run(provider+"/"+outcome, func(t *testing.T) {
				f := newTransactionFixture(t, provider)
				entered, release := make(chan struct{}), make(chan struct{})
				transactionServer(t, provider, func(w http.ResponseWriter, r *http.Request) {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					if outcome == "rejection" {
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
						return
					}
					transactionResponse(w)
				})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result := make(chan error, 1)
				go func() {
					result <- RefreshProfile(ctx, provider, "work", f.vault, f.health, WithProfileStore(f.profiles))
				}()
				awaitTransaction(t, entered)
				replacement := bytes.ReplaceAll(f.original, []byte("SYNTHETIC-OLD"), []byte("SYNTHETIC-RELOGIN"))
				temp := f.source + ".replacement"
				writeTransactionFile(t, temp, replacement)
				if err := os.Rename(temp, f.source); err != nil {
					t.Fatal(err)
				}
				before, _ := os.Stat(f.source)
				close(release)
				if err := <-result; !errors.Is(err, ErrCredentialChanged) || !IsSkipped(err) {
					t.Fatalf("replaced source result = %v, want changed-credential skip", err)
				}
				current, _ := os.ReadFile(f.source)
				after, _ := os.Stat(f.source)
				if !bytes.Equal(current, replacement) || !os.SameFile(before, after) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
					t.Fatal("old response modified replacement credential or metadata")
				}
				for _, path := range []string{f.live, f.home} {
					got, _ := os.ReadFile(path)
					if !bytes.Equal(got, f.original) {
						t.Fatal("discarded response propagated to a destination")
					}
				}
				stored, err := f.health.Load()
				if err != nil || stored.Profiles[provider+"/work"] != nil {
					t.Fatalf("obsolete response recorded a verdict on the replacement: %+v, %v", stored, err)
				}
			})
		}
	}
}

func TestRefreshDeliversOnlyCapturedCredentialGeneration(t *testing.T) {
	for _, provider := range []string{"codex", "gemini"} {
		for _, change := range []string{"none", "live during request", "isolated during request", "isolated different before request"} {
			t.Run(provider+"/"+change, func(t *testing.T) {
				f := newTransactionFixture(t, provider)
				other := bytes.ReplaceAll(f.original, []byte("SYNTHETIC-OLD"), []byte("SYNTHETIC-OTHER"))
				if change == "isolated different before request" {
					writeTransactionFile(t, f.home, other)
				}
				transactionServer(t, provider, func(w http.ResponseWriter, r *http.Request) {
					var changed string
					switch change {
					case "live during request":
						changed = f.live
					case "isolated during request":
						changed = f.home
					}
					if changed != "" {
						if err := os.WriteFile(changed, other, 0600); err != nil {
							t.Errorf("replace destination: %v", err)
							http.Error(w, "fixture write failed", http.StatusInternalServerError)
							return
						}
					}
					transactionResponse(w)
				})
				err := RefreshProfile(context.Background(), provider, "work", f.vault, f.health, WithProfileStore(f.profiles))
				partial := change == "live during request" || change == "isolated during request"
				if IsDeliveryIncomplete(err) != partial || (err != nil && !partial) || IsSkipped(err) {
					t.Fatalf("delivery result = %v, partial = %v", err, partial)
				}
				for _, path := range []string{f.source, f.live, f.home} {
					got, readErr := os.ReadFile(path)
					preserved := (path == f.live && change == "live during request") || (path == f.home && change != "none" && change != "live during request")
					if readErr != nil || (preserved && !bytes.Equal(got, other)) || (!preserved && !bytes.Contains(got, []byte("SYNTHETIC-NEW-ACCESS"))) {
						t.Fatalf("incorrect delivery to %s (preserve %v): %v", filepath.Base(filepath.Dir(path)), preserved, readErr)
					}
				}
				h, healthErr := f.health.GetProfile(provider, "work")
				if healthErr != nil || h == nil || h.ProviderRejected() || h.ProviderVerifiedAt().IsZero() || !h.TokenRenewable || time.Until(h.TokenExpiresAt) < 59*time.Minute {
					t.Fatalf("published opaque token lacks fresh verified health: %+v, %v", h, healthErr)
				}
			})
		}
	}
}

func TestRefreshWaitingForSameSourceHonorsCancellation(t *testing.T) {
	f := newTransactionFixture(t, "codex")
	var requests atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	transactionServer(t, "codex", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		transactionResponse(w)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := make(chan error, 1)
	go func() { first <- RefreshProfile(ctx, "codex", "work", f.vault, f.health, WithProfileStore(f.profiles)) }()
	awaitTransaction(t, entered)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer waitCancel()
	if err := RefreshProfile(waitCtx, "codex", "work", f.vault, f.health, WithProfileStore(f.profiles)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting refresh = %v, want context deadline", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("same rotating credential exchanged %d times", requests.Load())
	}
}

type observedLockWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *observedLockWaitContext) Done() <-chan struct{} {
	// Refresh first consults Done when the held file lock makes it wait.
	// This signals a captured, waiting invocation without timing assumptions.
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestRefreshWaitingForSameSourceSkipsCompletedGeneration(t *testing.T) {
	f := newTransactionFixture(t, "codex")
	var requests atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	transactionServer(t, "codex", func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		transactionResponse(w)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- RefreshProfile(ctx, "codex", "work", f.vault, f.health, WithProfileStore(f.profiles)) }()
	awaitTransaction(t, entered)
	waitCtx := &observedLockWaitContext{Context: ctx, waiting: make(chan struct{})}
	go func() {
		second <- RefreshProfile(waitCtx, "codex", "work", f.vault, f.health, WithProfileStore(f.profiles))
	}()
	awaitTransaction(t, waitCtx.waiting)
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; !errors.Is(err, ErrCredentialChanged) || !IsSkipped(err) {
		t.Fatalf("waiting refresh = %v, want a skipped superseded generation", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("same rotating credential exchanged %d times", requests.Load())
	}
	for _, path := range []string{f.source, f.live, f.home} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(got, []byte("SYNTHETIC-NEW-ACCESS")) {
			t.Fatalf("completed generation was not preserved: %v", err)
		}
	}
	h, err := f.health.GetProfile("codex", "work")
	if err != nil || h == nil || h.ProviderRejected() || h.ProviderVerifiedAt().IsZero() {
		t.Fatalf("waiting refresh changed the published verdict: %+v, %v", h, err)
	}
}

func TestRefreshInvalidSuccessPreservesSource(t *testing.T) {
	for _, provider := range []string{"codex", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			f := newTransactionFixture(t, provider)
			transactionServer(t, provider, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"expires_in":3600}`))
			})
			before, _ := os.Stat(f.source)
			if err := RefreshProfile(context.Background(), provider, "work", f.vault, f.health, WithProfileStore(f.profiles)); err == nil {
				t.Fatal("accepted a token endpoint response without an access token")
			}
			for _, path := range []string{f.source, f.live, f.home} {
				got, _ := os.ReadFile(path)
				if !bytes.Equal(got, f.original) {
					t.Fatal("invalid response changed credentials")
				}
			}
			after, _ := os.Stat(f.source)
			if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("invalid response rewrote credential metadata")
			}
			stored, _ := f.health.Load()
			if len(stored.Profiles) != 0 {
				t.Fatal("invalid response recorded provider acceptance")
			}
		})
	}
}

func TestRefreshUpdatesOnlyTheRequestedJSONGrant(t *testing.T) {
	for _, provider := range []string{"codex", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			f := newTransactionFixture(t, provider)
			var auth map[string]any
			if err := json.Unmarshal(f.original, &auth); err != nil {
				t.Fatal(err)
			}
			if provider == "codex" {
				auth["tokens"] = map[string]any{"access_token": "SYNTHETIC-NESTED-ACCESS", "refresh_token": "SYNTHETIC-NESTED-REFRESH"}
			} else {
				auth["oauth"] = map[string]any{"access_token": "SYNTHETIC-UNRELATED", "refresh_token": "SYNTHETIC-UNRELATED-REFRESH"}
			}
			body, err := json.Marshal(auth)
			if err != nil {
				t.Fatal(err)
			}
			writeTransactionFile(t, f.source, body)
			transactionServer(t, provider, func(w http.ResponseWriter, r *http.Request) {
				if provider == "codex" {
					var request map[string]string
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["refresh_token"] != "SYNTHETIC-NESTED-REFRESH" {
						t.Errorf("Codex request borrowed the obsolete flat grant: %v", err)
					}
				} else if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "SYNTHETIC-OLD-REFRESH" {
					t.Errorf("Gemini request selected another grant: %v", err)
				}
				transactionResponse(w)
			})
			if err := RefreshProfile(context.Background(), provider, "work", f.vault, f.health, WithProfileStore(f.profiles)); err != nil {
				t.Fatal(err)
			}
			updated, err := os.ReadFile(f.source)
			if err != nil || json.Unmarshal(updated, &auth) != nil {
				t.Fatalf("read published grant: %v", err)
			}
			if provider == "codex" {
				if auth["access_token"] != "SYNTHETIC-OLD-ACCESS" || auth["tokens"].(map[string]any)["access_token"] != "SYNTHETIC-NEW-ACCESS" {
					t.Fatal("Codex response updated the wrong JSON grant")
				}
			} else if auth["access_token"] != "SYNTHETIC-NEW-ACCESS" || auth["oauth"].(map[string]any)["access_token"] != "SYNTHETIC-UNRELATED" {
				t.Fatal("Gemini response updated the wrong JSON grant")
			}
		})
	}
}

func TestGeminiRefreshRejectsNewCanonicalSourceDuringLegacyExchange(t *testing.T) {
	f := newTransactionFixture(t, "gemini")
	legacy := filepath.Join(filepath.Dir(f.source), "oauth_credentials.json")
	if err := os.Rename(f.source, legacy); err != nil {
		t.Fatal(err)
	}
	newLogin := bytes.ReplaceAll(f.original, []byte("SYNTHETIC-OLD"), []byte("SYNTHETIC-RELOGIN"))
	transactionServer(t, "gemini", func(w http.ResponseWriter, _ *http.Request) {
		if err := os.WriteFile(f.source, newLogin, 0600); err != nil {
			t.Errorf("create canonical login: %v", err)
			http.Error(w, "fixture write failed", http.StatusInternalServerError)
			return
		}
		transactionResponse(w)
	})
	if err := RefreshProfile(context.Background(), "gemini", "work", f.vault, f.health, WithProfileStore(f.profiles)); !errors.Is(err, ErrCredentialChanged) {
		t.Fatalf("new canonical source did not obsolete legacy request: %v", err)
	}
	for path, want := range map[string][]byte{f.source: newLogin, legacy: f.original, f.live: f.original, f.home: f.original} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("legacy exchange altered a credential source: %v", err)
		}
	}
}

func TestRefreshSnapshotRejectsRetargetedSymlinkAndHardlink(t *testing.T) {
	for _, scenario := range []string{"retargeted symlink", "hardlink"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransactionFixture(t, "codex")
			other := f.source + ".other"
			writeTransactionFile(t, other, f.original)
			if scenario == "hardlink" {
				if err := os.Link(f.source, f.source+".alias"); err != nil {
					t.Skipf("hardlinks unavailable: %v", err)
				}
				if _, err := readCredentialSnapshot(f.source); err == nil {
					t.Fatal("shared hardlink could bypass canonical-path refresh locking")
				}
				return
			}
			link := f.source + ".link"
			if err := os.Symlink(f.source, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			snapshot, err := readCredentialSnapshot(link)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(link, link+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(other, link); err != nil {
				t.Fatal(err)
			}
			if err := snapshot.publish([]byte(`{"access_token":"SYNTHETIC-UNSAFE"}`)); !errors.Is(err, ErrCredentialChanged) {
				t.Fatalf("retargeted alias accepted: %v", err)
			}
			for _, path := range []string{f.source, other} {
				got, _ := os.ReadFile(path)
				if !bytes.Equal(got, f.original) {
					t.Fatal("retargeted symlink changed either credential")
				}
			}
		})
	}
}

func TestRefreshLockProcessHelper(t *testing.T) {
	path := os.Getenv("CAAM_REFRESH_LOCK_TEST_FILE")
	if path == "" {
		return
	}
	source, err := readCredentialSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	release, err := acquireRefreshLock(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Println("locked")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshLockExcludesAnotherProcess(t *testing.T) {
	f := newTransactionFixture(t, "codex")
	source, err := readCredentialSnapshot(f.source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRefreshLockProcessHelper$")
	command.Env = append(os.Environ(), "CAAM_REFRESH_LOCK_TEST_FILE="+f.source)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = command.Wait() }()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("child failed to acquire lock: %q, %v", line, err)
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer waitCancel()
	unlock, err := acquireRefreshLock(waitCtx, source)
	if unlock != nil {
		unlock()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cross-process lock = %v, want deadline", err)
	}
	if _, err := io.WriteString(input, "release\n"); err != nil {
		t.Fatal(err)
	}
}
