package config

import "github.com/zalando/go-keyring"

const keyringService = "spidey"

// GetAPIKey retrieves an API key from the OS credential store.
func GetAPIKey(adapter string) (string, error) {
	return keyring.Get(keyringService, adapter)
}

// SetAPIKey stores an API key in the OS credential store.
func SetAPIKey(adapter, key string) error {
	return keyring.Set(keyringService, adapter, key)
}

// DeleteAPIKey removes an API key from the OS credential store.
func DeleteAPIKey(adapter string) error {
	return keyring.Delete(keyringService, adapter)
}
