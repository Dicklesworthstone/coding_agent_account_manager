//go:build !darwin

package keychain

// Available is always false off darwin: no other platform caam supports keeps
// Claude Code credentials outside the filesystem.
func Available() bool { return false }

// Get is unsupported off darwin.
func Get(service, account string) ([]byte, error) { return nil, ErrUnsupported }

// Set is unsupported off darwin.
func Set(service, account string, data []byte) error { return ErrUnsupported }

// Delete is unsupported off darwin.
func Delete(service, account string) error { return ErrUnsupported }
