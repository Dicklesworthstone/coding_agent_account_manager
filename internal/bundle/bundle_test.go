package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewManifest(t *testing.T) {
	m := NewManifest()

	if m.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", m.SchemaVersion, CurrentSchemaVersion)
	}

	if m.CAAMVersion == "" {
		t.Error("CAAMVersion should not be empty")
	}

	if m.ExportTimestamp.IsZero() {
		t.Error("ExportTimestamp should not be zero")
	}

	if m.Checksums.Algorithm != "sha256" {
		t.Errorf("Checksums.Algorithm = %q, want %q", m.Checksums.Algorithm, "sha256")
	}

	if m.Checksums.Files == nil {
		t.Error("Checksums.Files should be initialized")
	}
}

func TestManifestAddProfile(t *testing.T) {
	m := NewManifest()

	m.AddProfile("claude", "alice@gmail.com")
	m.AddProfile("claude", "bob@gmail.com")
	m.AddProfile("codex", "work@company.com")

	if !m.Contents.Vault.Included {
		t.Error("Vault.Included should be true after adding profiles")
	}

	if m.Contents.Vault.TotalProfiles != 3 {
		t.Errorf("TotalProfiles = %d, want %d", m.Contents.Vault.TotalProfiles, 3)
	}

	claudeProfiles := m.Contents.Vault.Profiles["claude"]
	if len(claudeProfiles) != 2 {
		t.Errorf("len(claude profiles) = %d, want %d", len(claudeProfiles), 2)
	}

	codexProfiles := m.Contents.Vault.Profiles["codex"]
	if len(codexProfiles) != 1 {
		t.Errorf("len(codex profiles) = %d, want %d", len(codexProfiles), 1)
	}
}

func TestManifestAddChecksum(t *testing.T) {
	m := NewManifest()

	m.AddChecksum("vault/claude/alice/.claude.json", "abc123def456")
	m.AddChecksum("config.yaml", "xyz789")

	if len(m.Checksums.Files) != 2 {
		t.Errorf("len(Checksums.Files) = %d, want %d", len(m.Checksums.Files), 2)
	}

	if m.Checksums.Files["vault/claude/alice/.claude.json"] != "abc123def456" {
		t.Error("Checksum not stored correctly")
	}
}

func TestValidateManifest(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*ManifestV1)
		wantErr bool
	}{
		{
			name:    "valid manifest",
			modify:  func(m *ManifestV1) { m.Source.Hostname = "testhost" },
			wantErr: false,
		},
		{
			name:    "nil manifest",
			modify:  nil,
			wantErr: true,
		},
		{
			name: "missing schema version",
			modify: func(m *ManifestV1) {
				m.Source.Hostname = "testhost"
				m.SchemaVersion = 0
			},
			wantErr: true,
		},
		{
			name: "missing caam version",
			modify: func(m *ManifestV1) {
				m.Source.Hostname = "testhost"
				m.CAAMVersion = ""
			},
			wantErr: true,
		},
		{
			name: "missing export timestamp",
			modify: func(m *ManifestV1) {
				m.Source.Hostname = "testhost"
				m.ExportTimestamp = time.Time{}
			},
			wantErr: true,
		},
		{
			name: "missing hostname",
			modify: func(m *ManifestV1) {
				m.Source.Hostname = ""
			},
			wantErr: true,
		},
		{
			name: "invalid platform",
			modify: func(m *ManifestV1) {
				m.Source.Hostname = "testhost"
				m.Source.Platform = "invalid"
			},
			wantErr: true,
		},
		{
			name: "invalid arch",
			modify: func(m *ManifestV1) {
				m.Source.Hostname = "testhost"
				m.Source.Arch = "invalid"
			},
			wantErr: true,
		},
		{
			name: "invalid checksum algorithm",
			modify: func(m *ManifestV1) {
				m.Source.Hostname = "testhost"
				m.Checksums.Algorithm = "md5"
			},
			wantErr: true,
		},
		{
			name: "invalid checksum length",
			modify: func(m *ManifestV1) {
				m.Source.Hostname = "testhost"
				m.Checksums.Algorithm = "sha256"
				m.Checksums.Files = map[string]string{
					"test.txt": "tooshort",
				}
			},
			wantErr: true,
		},
		{
			name: "non-hex checksum",
			modify: func(m *ManifestV1) {
				m.Source.Hostname = "testhost"
				m.Checksums.Algorithm = "sha256"
				m.Checksums.Files = map[string]string{
					"test.txt": "gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg",
				}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m *ManifestV1
			if tt.modify != nil {
				m = NewManifest()
				tt.modify(m)
			}

			err := ValidateManifest(m)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateManifest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateManifestPortablePaths(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../work", `..\work`, "/absolute", `C:work`, `C:\work`, `\\server\share`, "work:stream", "work.", "work ", "CON", "nul.json", "COM1", "LPT²", "work\x00name"} {
		for _, field := range []string{"provider", "profile", "config", "checksum"} {
			t.Run(field+"/"+name, func(t *testing.T) {
				m := NewManifest()
				m.Source.Hostname = "test"
				switch field {
				case "provider":
					m.AddProfile(name, "work")
				case "profile":
					m.AddProfile("codex", name)
				case "config":
					m.SetConfig(true, name)
				case "checksum":
					m.AddChecksum(name, strings.Repeat("0", 64))
				}
				if err := ValidateManifest(m); err == nil {
					t.Fatal("nonportable path accepted")
				}
			})
		}
	}
	for _, name := range []string{"work", "alice@example.com", "release..candidate", "工作", "work.bak"} {
		m := NewManifest()
		m.Source.Hostname = "test"
		m.AddProfile("codex", name)
		m.SetSyncConfig(true, "sync/")
		if err := ValidateManifest(m); err != nil {
			t.Errorf("valid portable profile %q rejected: %v", name, err)
		}
	}
}

func TestIsCompatibleVersion(t *testing.T) {
	tests := []struct {
		name          string
		schemaVersion int
		wantErr       bool
	}{
		{
			name:          "current version",
			schemaVersion: CurrentSchemaVersion,
			wantErr:       false,
		},
		{
			name:          "future version",
			schemaVersion: CurrentSchemaVersion + 1,
			wantErr:       true,
		},
		{
			name:          "zero version",
			schemaVersion: 0,
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManifest()
			m.SchemaVersion = tt.schemaVersion

			err := IsCompatibleVersion(m)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsCompatibleVersion() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestComputeFileChecksum(t *testing.T) {
	// Create a temp file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	testData := []byte("hello world")

	if err := os.WriteFile(testFile, testData, 0600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	checksum, err := ComputeFileChecksum(testFile, AlgorithmSHA256)
	if err != nil {
		t.Fatalf("ComputeFileChecksum() error = %v", err)
	}

	// Known SHA256 of "hello world"
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if checksum != expected {
		t.Errorf("checksum = %q, want %q", checksum, expected)
	}
}

func TestComputeDataChecksum(t *testing.T) {
	data := []byte("hello world")

	checksum, err := ComputeDataChecksum(data, AlgorithmSHA256)
	if err != nil {
		t.Fatalf("ComputeDataChecksum() error = %v", err)
	}

	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if checksum != expected {
		t.Errorf("checksum = %q, want %q", checksum, expected)
	}
}

func TestComputeDirectoryChecksums(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some test files
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	files := map[string][]byte{
		"file1.txt":        []byte("content1"),
		"subdir/file2.txt": []byte("content2"),
	}

	for relPath, content := range files {
		path := filepath.Join(tmpDir, relPath)
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}

	checksums, err := ComputeDirectoryChecksums(tmpDir, AlgorithmSHA256)
	if err != nil {
		t.Fatalf("ComputeDirectoryChecksums() error = %v", err)
	}

	if len(checksums) != 2 {
		t.Errorf("len(checksums) = %d, want %d", len(checksums), 2)
	}

	// Verify all paths are normalized (forward slashes)
	for path := range checksums {
		if filepath.IsAbs(path) {
			t.Errorf("path %q should be relative", path)
		}
	}
}

func TestVerifyChecksums(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files
	testFile := filepath.Join(tmpDir, "test.txt")
	testData := []byte("hello")
	if err := os.WriteFile(testFile, testData, 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Compute correct checksum
	correctChecksum, _ := ComputeFileChecksum(testFile, AlgorithmSHA256)

	t.Run("valid checksums", func(t *testing.T) {
		m := NewManifest()
		m.AddChecksum("test.txt", correctChecksum)

		result, err := VerifyChecksums(tmpDir, m)
		if err != nil {
			t.Fatalf("VerifyChecksums() error = %v", err)
		}

		if !result.Valid {
			t.Errorf("result.Valid = %v, want %v", result.Valid, true)
		}

		if len(result.Verified) != 1 {
			t.Errorf("len(Verified) = %d, want %d", len(result.Verified), 1)
		}
	})

	t.Run("mismatched checksum", func(t *testing.T) {
		m := NewManifest()
		m.AddChecksum("test.txt", "0000000000000000000000000000000000000000000000000000000000000000")

		result, err := VerifyChecksums(tmpDir, m)
		if err != nil {
			t.Fatalf("VerifyChecksums() error = %v", err)
		}

		if result.Valid {
			t.Errorf("result.Valid = %v, want %v", result.Valid, false)
		}

		if len(result.Mismatch) != 1 {
			t.Errorf("len(Mismatch) = %d, want %d", len(result.Mismatch), 1)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		m := NewManifest()
		m.AddChecksum("missing.txt", correctChecksum)

		result, err := VerifyChecksums(tmpDir, m)
		if err != nil {
			t.Fatalf("VerifyChecksums() error = %v", err)
		}

		if result.Valid {
			t.Errorf("result.Valid = %v, want %v", result.Valid, false)
		}

		if len(result.Missing) != 1 {
			t.Errorf("len(Missing) = %d, want %d", len(result.Missing), 1)
		}
	})
	t.Run("unchecked payload", func(t *testing.T) {
		result, err := VerifyChecksums(tmpDir, NewManifest())
		if err != nil {
			t.Fatal(err)
		}
		if result.Valid || len(result.Extra) != 1 || result.Summary() != "Verification failed: 1 extra" {
			t.Fatalf("unchecked payload reported as verified: %+v (%s)", result, result.Summary())
		}
	})
}

func TestEncryptDecryptBundle(t *testing.T) {
	plainData := []byte("this is sensitive data that needs encryption")
	password := "correct-horse-battery-staple"

	// Encrypt
	ciphertext, meta, err := EncryptBundle(plainData, password)
	if err != nil {
		t.Fatalf("EncryptBundle() error = %v", err)
	}

	if len(ciphertext) == 0 {
		t.Error("ciphertext should not be empty")
	}

	if meta == nil {
		t.Fatal("metadata should not be nil")
	}

	if meta.Algorithm != "aes-256-gcm" {
		t.Errorf("Algorithm = %q, want %q", meta.Algorithm, "aes-256-gcm")
	}

	if meta.KDF != "argon2id" {
		t.Errorf("KDF = %q, want %q", meta.KDF, "argon2id")
	}

	// Decrypt with correct password
	decrypted, err := DecryptBundle(ciphertext, meta, password)
	if err != nil {
		t.Fatalf("DecryptBundle() error = %v", err)
	}

	if string(decrypted) != string(plainData) {
		t.Errorf("decrypted = %q, want %q", decrypted, plainData)
	}

	// Decrypt with wrong password
	_, err = DecryptBundle(ciphertext, meta, "wrong-password")
	if err == nil {
		t.Error("DecryptBundle() should fail with wrong password")
	}
}

func TestEncryptBundleEmptyPassword(t *testing.T) {
	_, _, err := EncryptBundle([]byte("data"), "")
	if err == nil {
		t.Error("EncryptBundle() should fail with empty password")
	}
}

func TestDecryptBundleNilMeta(t *testing.T) {
	_, err := DecryptBundle([]byte("data"), nil, "password")
	if err == nil {
		t.Error("DecryptBundle() should fail with nil metadata")
	}
}

func TestValidateEncryptionMetadata(t *testing.T) {
	tests := []struct {
		name    string
		meta    *EncryptionMetadata
		wantErr bool
	}{
		{
			name: "valid metadata",
			meta: &EncryptionMetadata{
				Version:      1,
				Algorithm:    "aes-256-gcm",
				KDF:          "argon2id",
				Salt:         "dGVzdHNhbHQ=",
				Nonce:        "dGVzdG5vbmNl",
				Argon2Params: DefaultArgon2Params(),
			},
			wantErr: false,
		},
		{
			name:    "nil metadata",
			meta:    nil,
			wantErr: true,
		},
		{
			name: "unsupported algorithm",
			meta: &EncryptionMetadata{
				Version:   1,
				Algorithm: "aes-128-cbc",
				KDF:       "argon2id",
				Salt:      "dGVzdHNhbHQ=",
				Nonce:     "dGVzdG5vbmNl",
			},
			wantErr: true,
		},
		{
			name: "unsupported KDF",
			meta: &EncryptionMetadata{
				Version:   1,
				Algorithm: "aes-256-gcm",
				KDF:       "pbkdf2",
				Salt:      "dGVzdHNhbHQ=",
				Nonce:     "dGVzdG5vbmNl",
			},
			wantErr: true,
		},
		{
			name: "missing salt",
			meta: &EncryptionMetadata{
				Version:   1,
				Algorithm: "aes-256-gcm",
				KDF:       "argon2id",
				Nonce:     "dGVzdG5vbmNl",
			},
			wantErr: true,
		},
		{
			name: "missing nonce",
			meta: &EncryptionMetadata{
				Version:   1,
				Algorithm: "aes-256-gcm",
				KDF:       "argon2id",
				Salt:      "dGVzdHNhbHQ=",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEncryptionMetadata(tt.meta)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateEncryptionMetadata() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"foo/bar", "foo/bar"},
		{"foo\\bar", "foo/bar"},
		{"foo\\bar\\baz", "foo/bar/baz"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizePath(tt.input)
			if got != tt.want {
				t.Errorf("NormalizePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDefaultArgon2Params(t *testing.T) {
	params := DefaultArgon2Params()

	if params.Time < 1 {
		t.Error("Time should be >= 1")
	}

	if params.Memory < 1024 {
		t.Error("Memory should be >= 1024 (1 MiB)")
	}

	if params.Threads < 1 {
		t.Error("Threads should be >= 1")
	}

	if params.KeyLen != 32 {
		t.Errorf("KeyLen = %d, want %d (for AES-256)", params.KeyLen, 32)
	}
}

func TestSaveAndLoadManifest(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a manifest
	m := NewManifest()
	m.Source.Hostname = "test-host"
	m.Source.Platform = "linux"
	m.Source.Arch = "amd64"
	m.AddProfile("claude", "alice@gmail.com")

	// Save it
	if err := SaveManifest(tmpDir, m); err != nil {
		t.Fatalf("SaveManifest() error = %v", err)
	}

	// Load it back
	loaded, err := LoadManifest(tmpDir)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}

	// Verify
	if loaded.Source.Hostname != m.Source.Hostname {
		t.Errorf("Hostname = %q, want %q", loaded.Source.Hostname, m.Source.Hostname)
	}

	if loaded.Contents.Vault.TotalProfiles != 1 {
		t.Errorf("TotalProfiles = %d, want %d", loaded.Contents.Vault.TotalProfiles, 1)
	}
}

func TestSaveAndLoadEncryptionMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	meta := &EncryptionMetadata{
		Version:      1,
		Algorithm:    "aes-256-gcm",
		KDF:          "argon2id",
		Salt:         "dGVzdHNhbHQ=",
		Nonce:        "dGVzdG5vbmNl",
		Argon2Params: DefaultArgon2Params(),
	}

	// Save it
	if err := SaveEncryptionMetadata(tmpDir, meta); err != nil {
		t.Fatalf("SaveEncryptionMetadata() error = %v", err)
	}

	// Load it back
	loaded, err := LoadEncryptionMetadata(tmpDir)
	if err != nil {
		t.Fatalf("LoadEncryptionMetadata() error = %v", err)
	}

	if loaded == nil {
		t.Fatal("loaded metadata should not be nil")
	}

	if loaded.Algorithm != meta.Algorithm {
		t.Errorf("Algorithm = %q, want %q", loaded.Algorithm, meta.Algorithm)
	}

	if loaded.Salt != meta.Salt {
		t.Errorf("Salt = %q, want %q", loaded.Salt, meta.Salt)
	}
}

func TestIsEncrypted(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("not encrypted", func(t *testing.T) {
		encrypted, err := IsEncrypted(tmpDir)
		if err != nil {
			t.Fatalf("IsEncrypted() error = %v", err)
		}
		if encrypted {
			t.Error("should not be encrypted")
		}
	})

	t.Run("encrypted directory", func(t *testing.T) {
		// Create marker file
		markerPath := filepath.Join(tmpDir, EncryptionMarkerFile)
		if err := os.WriteFile(markerPath, []byte("{}"), 0600); err != nil {
			t.Fatalf("write marker: %v", err)
		}

		encrypted, err := IsEncrypted(tmpDir)
		if err != nil {
			t.Fatalf("IsEncrypted() error = %v", err)
		}
		if !encrypted {
			t.Error("should be encrypted")
		}
	})

	t.Run("encrypted filename", func(t *testing.T) {
		encryptedPath := filepath.Join(tmpDir, "bundle.enc.zip")
		encrypted, err := IsEncrypted(encryptedPath)
		if err != nil {
			t.Fatalf("IsEncrypted() error = %v", err)
		}
		if !encrypted {
			t.Error("should be detected as encrypted by filename")
		}
	})
}

func TestVerificationResultSummary(t *testing.T) {
	tests := []struct {
		name   string
		result *VerificationResult
		want   string
	}{
		{
			name: "all valid",
			result: &VerificationResult{
				Valid:    true,
				Verified: []string{"a", "b", "c"},
			},
			want: "Verified 3 files, all checksums match",
		},
		{
			name: "missing only",
			result: &VerificationResult{
				Valid:   false,
				Missing: []string{"a"},
			},
			want: "Verification failed: 1 missing",
		},
		{
			name: "corrupted only",
			result: &VerificationResult{
				Valid:    false,
				Mismatch: []ChecksumMismatch{{Path: "a"}},
			},
			want: "Verification failed: 1 corrupted",
		},
		{
			name: "missing and corrupted",
			result: &VerificationResult{
				Valid:    false,
				Missing:  []string{"a", "b"},
				Mismatch: []ChecksumMismatch{{Path: "c"}},
			},
			want: "Verification failed: 2 missing and 1 corrupted",
		},
		{
			name: "all three",
			result: &VerificationResult{
				Valid:    false,
				Missing:  []string{"a"},
				Mismatch: []ChecksumMismatch{{Path: "b"}},
				Extra:    []string{"c"},
			},
			want: "Verification failed: 1 missing, 1 corrupted, and 1 extra",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.result.Summary()
			if got != tt.want {
				t.Errorf("Summary() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Tests for GenerateRandomBytes
func TestGenerateRandomBytes(t *testing.T) {
	t.Run("generates correct length", func(t *testing.T) {
		for _, size := range []int{1, 16, 32, 64, 128} {
			bytes, err := GenerateRandomBytes(size)
			if err != nil {
				t.Fatalf("GenerateRandomBytes(%d) error = %v", size, err)
			}
			if len(bytes) != size {
				t.Errorf("GenerateRandomBytes(%d) len = %d, want %d", size, len(bytes), size)
			}
		}
	})

	t.Run("generates different values", func(t *testing.T) {
		bytes1, _ := GenerateRandomBytes(32)
		bytes2, _ := GenerateRandomBytes(32)

		// Two random 32-byte slices should be different
		same := true
		for i := range bytes1 {
			if bytes1[i] != bytes2[i] {
				same = false
				break
			}
		}
		if same {
			t.Error("Two calls to GenerateRandomBytes produced identical results")
		}
	})

	t.Run("zero length", func(t *testing.T) {
		bytes, err := GenerateRandomBytes(0)
		if err != nil {
			t.Fatalf("GenerateRandomBytes(0) error = %v", err)
		}
		if len(bytes) != 0 {
			t.Errorf("GenerateRandomBytes(0) len = %d, want 0", len(bytes))
		}
	})
}

// Tests for SecureWipe
func TestSecureWipe(t *testing.T) {
	t.Run("wipes data", func(t *testing.T) {
		data := []byte("sensitive data that should be wiped")
		original := make([]byte, len(data))
		copy(original, data)

		SecureWipe(data)

		// All bytes should now be zero
		for i, b := range data {
			if b != 0 {
				t.Errorf("byte at index %d = %d, want 0", i, b)
			}
		}
	})

	t.Run("handles empty slice", func(t *testing.T) {
		data := []byte{}
		SecureWipe(data) // Should not panic
	})

	t.Run("handles nil slice", func(t *testing.T) {
		var data []byte
		SecureWipe(data) // Should not panic
	})
}

// Tests for NewEncryptionMetadata
func TestNewEncryptionMetadata(t *testing.T) {
	salt := []byte("testsalt12345678testsalt12345678") // 32 bytes
	nonce := []byte("testnonce123")                    // 12 bytes

	meta := NewEncryptionMetadata(salt, nonce)

	if meta.Version != 1 {
		t.Errorf("Version = %d, want 1", meta.Version)
	}

	if meta.Algorithm != "aes-256-gcm" {
		t.Errorf("Algorithm = %q, want %q", meta.Algorithm, "aes-256-gcm")
	}

	if meta.KDF != "argon2id" {
		t.Errorf("KDF = %q, want %q", meta.KDF, "argon2id")
	}

	if meta.Salt == "" {
		t.Error("Salt should be set")
	}

	if meta.Nonce == "" {
		t.Error("Nonce should be set")
	}

	if meta.Argon2Params == nil {
		t.Error("Argon2Params should be set")
	}
}

// Tests for NewEncryptionMetadataDefaults
func TestNewEncryptionMetadataDefaults(t *testing.T) {
	meta := NewEncryptionMetadataDefaults()

	if meta.Version != 1 {
		t.Errorf("Version = %d, want 1", meta.Version)
	}

	if meta.Algorithm != "aes-256-gcm" {
		t.Errorf("Algorithm = %q, want %q", meta.Algorithm, "aes-256-gcm")
	}

	if meta.KDF != "argon2id" {
		t.Errorf("KDF = %q, want %q", meta.KDF, "argon2id")
	}

	// Salt and Nonce should be empty for defaults
	if meta.Salt != "" {
		t.Errorf("Salt = %q, want empty", meta.Salt)
	}

	if meta.Nonce != "" {
		t.Errorf("Nonce = %q, want empty", meta.Nonce)
	}

	if meta.Argon2Params == nil {
		t.Error("Argon2Params should be set")
	}
}

// Tests for ValidationError.Error()
func TestValidationErrorError(t *testing.T) {
	tests := []struct {
		name string
		err  *ValidationError
		want string
	}{
		{
			name: "with field",
			err: &ValidationError{
				Field:   "schema_version",
				Message: "must be >= 1",
			},
			want: "schema_version: must be >= 1",
		},
		{
			name: "without field",
			err: &ValidationError{
				Message: "manifest is nil",
			},
			want: "manifest is nil",
		},
		{
			name: "empty field",
			err: &ValidationError{
				Field:   "",
				Message: "some error",
			},
			want: "some error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Tests for validateArgon2Params edge cases
func TestValidateArgon2ParamsEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		params  *Argon2Params
		wantErr bool
	}{
		{
			name:    "nil params is allowed",
			params:  nil,
			wantErr: false, // Nil params is accepted; only validated when non-nil
		},
		{
			name: "zero time",
			params: &Argon2Params{
				Time:    0,
				Memory:  64 * 1024,
				Threads: 4,
				KeyLen:  32,
			},
			wantErr: true,
		},
		{
			name: "memory too low",
			params: &Argon2Params{
				Time:    3,
				Memory:  512, // Less than 1024 (1 MiB)
				Threads: 4,
				KeyLen:  32,
			},
			wantErr: true,
		},
		{
			name: "zero threads",
			params: &Argon2Params{
				Time:    3,
				Memory:  64 * 1024,
				Threads: 0,
				KeyLen:  32,
			},
			wantErr: true,
		},
		{
			name: "key length too small",
			params: &Argon2Params{
				Time:    3,
				Memory:  64 * 1024,
				Threads: 4,
				KeyLen:  8, // Less than 16
			},
			wantErr: true,
		},
		{
			name: "key length too large",
			params: &Argon2Params{
				Time:    3,
				Memory:  64 * 1024,
				Threads: 4,
				KeyLen:  128, // More than 64
			},
			wantErr: true,
		},
		{
			name: "valid min key length",
			params: &Argon2Params{
				Time:    3,
				Memory:  64 * 1024,
				Threads: 4,
				KeyLen:  16, // Minimum valid
			},
			wantErr: false,
		},
		{
			name:    "valid default params",
			params:  DefaultArgon2Params(),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create metadata with the params
			meta := &EncryptionMetadata{
				Version:      1,
				Algorithm:    "aes-256-gcm",
				KDF:          "argon2id",
				Salt:         "dGVzdHNhbHQ=",
				Nonce:        "dGVzdG5vbmNl",
				Argon2Params: tt.params,
			}

			err := ValidateEncryptionMetadata(meta)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateEncryptionMetadata() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// Tests for AddChecksum edge cases
func TestAddChecksumEdgeCases(t *testing.T) {
	t.Run("nil files map", func(t *testing.T) {
		m := &ManifestV1{}
		m.Checksums.Files = nil

		m.AddChecksum("test.txt", "abc123")

		if m.Checksums.Files == nil {
			t.Error("Files map should be initialized")
		}

		if m.Checksums.Files["test.txt"] != "abc123" {
			t.Error("Checksum should be stored")
		}
	})

	t.Run("overwrite existing checksum", func(t *testing.T) {
		m := NewManifest()
		m.AddChecksum("test.txt", "old_checksum")
		m.AddChecksum("test.txt", "new_checksum")

		if m.Checksums.Files["test.txt"] != "new_checksum" {
			t.Errorf("Checksum = %q, want %q", m.Checksums.Files["test.txt"], "new_checksum")
		}
	})
}

// Tests for DecryptBundle edge cases
func TestDecryptBundleEdgeCases(t *testing.T) {
	t.Run("empty password", func(t *testing.T) {
		meta := &EncryptionMetadata{
			Version:      1,
			Algorithm:    "aes-256-gcm",
			KDF:          "argon2id",
			Salt:         "dGVzdHNhbHQ=",
			Nonce:        "dGVzdG5vbmNl",
			Argon2Params: DefaultArgon2Params(),
		}

		_, err := DecryptBundle([]byte("data"), meta, "")
		if err == nil {
			t.Error("DecryptBundle should fail with empty password")
		}
	})

	t.Run("invalid base64 salt", func(t *testing.T) {
		meta := &EncryptionMetadata{
			Version:      1,
			Algorithm:    "aes-256-gcm",
			KDF:          "argon2id",
			Salt:         "!!!invalid-base64!!!",
			Nonce:        "dGVzdG5vbmNl",
			Argon2Params: DefaultArgon2Params(),
		}

		_, err := DecryptBundle([]byte("data"), meta, "password")
		if err == nil {
			t.Error("DecryptBundle should fail with invalid base64 salt")
		}
	})

	t.Run("invalid base64 nonce", func(t *testing.T) {
		meta := &EncryptionMetadata{
			Version:      1,
			Algorithm:    "aes-256-gcm",
			KDF:          "argon2id",
			Salt:         "dGVzdHNhbHQ=",
			Nonce:        "!!!invalid-base64!!!",
			Argon2Params: DefaultArgon2Params(),
		}

		_, err := DecryptBundle([]byte("data"), meta, "password")
		if err == nil {
			t.Error("DecryptBundle should fail with invalid base64 nonce")
		}
	})

	t.Run("nil argon2 params uses defaults", func(t *testing.T) {
		// First encrypt with known params
		plainData := []byte("test data")
		password := "testpass123"

		ciphertext, origMeta, err := EncryptBundle(plainData, password)
		if err != nil {
			t.Fatalf("EncryptBundle error = %v", err)
		}

		// Decrypt with nil Argon2Params (should use defaults)
		metaWithNilParams := &EncryptionMetadata{
			Version:      1,
			Algorithm:    "aes-256-gcm",
			KDF:          "argon2id",
			Salt:         origMeta.Salt,
			Nonce:        origMeta.Nonce,
			Argon2Params: nil,
		}

		decrypted, err := DecryptBundle(ciphertext, metaWithNilParams, password)
		if err != nil {
			t.Fatalf("DecryptBundle error = %v", err)
		}

		if string(decrypted) != string(plainData) {
			t.Errorf("Decrypted = %q, want %q", decrypted, plainData)
		}
	})
}

// Tests for LoadManifest edge cases
func TestLoadManifestEdgeCases(t *testing.T) {
	t.Run("nonexistent directory", func(t *testing.T) {
		_, err := LoadManifest("/nonexistent/path")
		if err == nil {
			t.Error("LoadManifest should fail for nonexistent path")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		tmpDir := t.TempDir()
		manifestPath := filepath.Join(tmpDir, ManifestFileName)

		if err := os.WriteFile(manifestPath, []byte("not valid json"), 0600); err != nil {
			t.Fatal(err)
		}

		_, err := LoadManifest(tmpDir)
		if err == nil {
			t.Error("LoadManifest should fail for invalid JSON")
		}
	})
}

// Tests for SaveManifest edge cases
func TestSaveManifestEdgeCases(t *testing.T) {
	t.Run("creates directory if missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		nestedDir := filepath.Join(tmpDir, "nested", "path")

		m := NewManifest()
		m.Source.Hostname = "testhost"

		if err := SaveManifest(nestedDir, m); err != nil {
			t.Errorf("SaveManifest error = %v", err)
		}

		// Verify file was created
		if _, err := os.Stat(filepath.Join(nestedDir, ManifestFileName)); os.IsNotExist(err) {
			t.Error("Manifest file should exist")
		}
	})

	t.Run("invalid manifest fails", func(t *testing.T) {
		tmpDir := t.TempDir()

		m := &ManifestV1{} // Invalid - missing required fields

		if err := SaveManifest(tmpDir, m); err == nil {
			t.Error("SaveManifest should fail for invalid manifest")
		}
	})
}

// Tests for IsCompatibleVersion edge cases
func TestIsCompatibleVersionEdgeCases(t *testing.T) {
	t.Run("negative version", func(t *testing.T) {
		m := NewManifest()
		m.SchemaVersion = -1

		err := IsCompatibleVersion(m)
		if err == nil {
			t.Error("IsCompatibleVersion should fail for negative version")
		}
	})
}

// Tests for newHasher edge cases
func TestNewHasherEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		algorithm ChecksumAlgorithm
		wantErr   bool
	}{
		{"sha256 constant", AlgorithmSHA256, false},
		{"sha256 string", ChecksumAlgorithm("sha256"), false},
		{"sha512 supported", AlgorithmSHA512, false},
		{"empty defaults to sha256", ChecksumAlgorithm(""), false},
		{"SHA256 uppercase rejected", ChecksumAlgorithm("SHA256"), true}, // Case sensitive
		{"md5 unsupported", ChecksumAlgorithm("md5"), true},
		{"unknown algorithm", ChecksumAlgorithm("blake2b"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test via ComputeDataChecksum since newHasher is internal
			_, err := ComputeDataChecksum([]byte("test"), tt.algorithm)
			if (err != nil) != tt.wantErr {
				t.Errorf("ComputeDataChecksum with algorithm %q error = %v, wantErr %v", tt.algorithm, err, tt.wantErr)
			}
		})
	}
}
