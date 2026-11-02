package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/vaulttransfer"
)

func TestTarImportPreservesConcurrentDestinationAndInterruptedReplacement(t *testing.T) {
	for _, failure := range []string{"before-publication", "publication-error", "new-login-during-publication"} {
		t.Run(failure, func(t *testing.T) {
			v := authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
			profile := v.ProfilePath("claude", "work")
			old := tarClaudeTestFiles(t, "old", "Personal")
			incoming := tarClaudeTestFiles(t, "incoming", "Team")
			concurrent := tarClaudeTestFiles(t, "independent", "Personal")
			tarWriteProfile(t, profile, old)
			before, err := vaulttransfer.Capture(profile)
			if err != nil {
				t.Fatal(err)
			}
			archive := tarRawFixture(t, map[string]map[string]string{"claude/work": incoming}, nil)
			opt := importOptions{Force: true}
			if failure == "before-publication" {
				opt.BeforePublish = func(_, destination string) { tarWriteProfile(t, destination, concurrent) }
			} else {
				opt.Rename = func(from, to string) error {
					if to == profile && strings.HasPrefix(filepath.Base(from), "_caam-vault-backup~import-") {
						if failure == "new-login-during-publication" {
							tarWriteProfile(t, profile, concurrent)
						}
						return errors.New("injected publication failure")
					}
					return os.Rename(from, to)
				}
			}
			result, err := importArchive(bytes.NewReader(archive), v, opt)
			if err == nil || result == nil || len(result.Profiles) != 1 || result.Profiles[0].Imported {
				t.Fatal("failed publication was reported as success")
			}
			if failure == "publication-error" {
				if err := before.Check(); err != nil {
					t.Fatalf("returned-error recovery did not restore the complete previous profile: %v", err)
				}
			} else {
				for name, contents := range concurrent {
					requireSwitchCredential(t, filepath.Join(profile, name), contents)
				}
			}
			if failure == "new-login-during-publication" {
				if result.Profiles[0].RecoveryPath == "" {
					t.Fatal("recovery path was lost after preserving an independent login")
				}
				if err := before.CheckAt(result.Profiles[0].RecoveryPath); err != nil {
					t.Fatalf("old snapshot was lost after a concurrent login: %v", err)
				}
			}
		})
	}
}

func TestTarExportRejectsChangedSourceGeneration(t *testing.T) {
	for _, changed := range []string{"credentials", "settings", "same-bytes-new-inode"} {
		t.Run(changed, func(t *testing.T) {
			v := authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
			profile := v.ProfilePath("claude", "work")
			files := tarClaudeTestFiles(t, "team", "Team")
			tarWriteProfile(t, profile, files)
			prepared, err := prepareExportArchive(v, exportRequest{Tool: "claude", Profile: "work"}, exportOptions{BeforeArchive: func() {
				switch changed {
				case "credentials":
					writeNativeTestCredential(t, filepath.Join(profile, ".credentials.json"), tarClaudeTestFiles(t, "rotated", "Team")[".credentials.json"])
				case "settings":
					writeNativeTestCredential(t, filepath.Join(profile, ".claude.json"), `{"oauthAccount":{"organizationUuid":"org-Personal"}}`)
				case "same-bytes-new-inode":
					name := filepath.Join(profile, ".credentials.json")
					writeNativeTestCredential(t, name+".replacement", files[".credentials.json"])
					if err := os.Rename(name+".replacement", name); err != nil {
						t.Fatal(err)
					}
				}
			}})
			if err == nil || prepared != nil {
				t.Fatal("a changed source generation produced a usable export")
			}
			if strings.Contains(err.Error(), "SYNTHETIC-") {
				t.Fatal("generation error disclosed credential bytes")
			}
		})
	}
}

func TestTarImportCapturesLatestDestinationAfterManagedLockWait(t *testing.T) {
	v := authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
	profile := v.ProfilePath("claude", "work")
	old := tarClaudeTestFiles(t, "old", "Team")
	rotated := tarClaudeTestFiles(t, "rotated", "Team")
	incoming := tarClaudeTestFiles(t, "incoming", "Personal")
	tarWriteProfile(t, profile, old)
	archive := tarRawFixture(t, map[string]map[string]string{"claude/work": incoming}, nil)
	lockHeld, update, writerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	var writerErr error
	go func() {
		defer close(writerDone)
		writerErr = v.WithMutationLock(func() error {
			close(lockHeld)
			<-update
			for name, data := range rotated {
				if err := os.WriteFile(filepath.Join(profile, name), []byte(data), 0600); err != nil {
					return err
				}
			}
			return nil
		})
	}()
	defer func() {
		release.Do(func() { close(update) })
		<-writerDone
	}()
	select {
	case <-lockHeld:
	case <-time.After(5 * time.Second):
		t.Fatal("managed writer did not acquire the lock")
	}
	beforeLock, importDone := make(chan struct{}), make(chan struct{})
	var result *importArchiveResult
	var importErr error
	go func() {
		defer close(importDone)
		result, importErr = importArchive(bytes.NewReader(archive), v, importOptions{Force: true, BeforeLock: func() { close(beforeLock) }})
	}()
	defer func() {
		release.Do(func() { close(update) })
		<-importDone
	}()
	select {
	case <-beforeLock:
	case <-importDone:
		t.Fatalf("import stopped before waiting for the lock: %v", importErr)
	case <-time.After(5 * time.Second):
		t.Fatal("import did not finish private preflight")
	}
	// Complete a legitimate rotation while import is about to wait for the
	// managed lock. Import must capture and retain this generation, not the old one.
	release.Do(func() { close(update) })
	<-writerDone
	<-importDone
	if writerErr != nil || importErr != nil {
		t.Fatalf("writer/import failed: %v; %v", writerErr, importErr)
	}
	if result == nil || len(result.Profiles) != 1 || result.Profiles[0].RecoveryPath == "" {
		t.Fatal("replacement did not report the rotated recovery generation")
	}
	for name, data := range rotated {
		requireSwitchCredential(t, filepath.Join(result.Profiles[0].RecoveryPath, name), data)
	}
	requireSwitchCredential(t, filepath.Join(profile, ".credentials.json"), incoming[".credentials.json"])
}

func TestTarImportChecksAllConflictsBeforeAnyPublication(t *testing.T) {
	v := authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
	old := tarClaudeTestFiles(t, "old", "Personal")
	incoming := tarClaudeTestFiles(t, "incoming", "Team")
	tarWriteProfile(t, v.ProfilePath("claude", "z-conflict"), old)
	before, err := vaulttransfer.Capture(v.ProfilePath("claude", "z-conflict"))
	if err != nil {
		t.Fatal(err)
	}
	archive := tarRawFixture(t, map[string]map[string]string{"claude/a-new": incoming, "claude/z-conflict": incoming}, nil)
	result, err := importArchive(bytes.NewReader(archive), v, importOptions{})
	if err == nil || result == nil || len(result.Profiles) != 0 {
		t.Fatal("existing later destination was not preflighted")
	}
	if _, err := os.Stat(v.ProfilePath("claude", "a-new")); !os.IsNotExist(err) {
		t.Fatal("earlier profile was published before a later conflict was checked")
	}
	if err := before.Check(); err != nil {
		t.Fatal(err)
	}
}
