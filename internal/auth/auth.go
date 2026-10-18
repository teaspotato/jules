// Package auth finds the Jules API key. It never writes the key to disk in
// plain text: it comes from JULES_API_KEY or the OS keychain.
package auth

import (
	"errors"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	EnvVar         = "JULES_API_KEY"
	keyringService = "jules-tui"
	keyringAccount = "api-key"
)

// ErrNoKey means neither the environment nor the keychain holds a key.
var ErrNoKey = errors.New("no Jules API key: set " + EnvVar + " or run `jules auth login`")

// Origin says where a key came from.
type Origin string

const (
	FromEnv      Origin = "environment (" + EnvVar + ")"
	FromKeychain Origin = "OS keychain"
)

// Key returns the API key and where it was found.
func Key() (string, Origin, error) {
	if k := strings.TrimSpace(os.Getenv(EnvVar)); k != "" {
		return k, FromEnv, nil
	}
	k, err := keyring.Get(keyringService, keyringAccount)
	if err == nil && k != "" {
		return k, FromKeychain, nil
	}
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return "", "", errors.Join(ErrNoKey, err)
	}
	return "", "", ErrNoKey
}

// Store saves key in the OS keychain.
func Store(key string) error {
	return keyring.Set(keyringService, keyringAccount, strings.TrimSpace(key))
}

// Delete removes the key from the OS keychain.
func Delete() error {
	err := keyring.Delete(keyringService, keyringAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// Mask shows only the last four characters of a key.
func Mask(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return strings.Repeat("*", 8) + key[len(key)-4:]
}
