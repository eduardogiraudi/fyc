package config

import (
	"fmt"
	"strings"

	"fuckyourcli/store"
)

// MCPOAuthStore keeps registration and tokens out of config.json.
type MCPOAuthStore struct{}

func (MCPOAuthStore) Load(key string) (string, error) {
	if !strings.HasPrefix(key, "FYC_MCP_OAUTH_") {
		return "", fmt.Errorf("invalid MCP OAuth credential key")
	}
	auth, corrupt := loadAuthStore()
	if corrupt {
		return "", fmt.Errorf("auth.json is unreadable")
	}
	return auth[key], nil
}

func (MCPOAuthStore) Save(key, value string) error {
	if !strings.HasPrefix(key, "FYC_MCP_OAUTH_") {
		return fmt.Errorf("invalid MCP OAuth credential key")
	}
	return SavePluginSecret(key, value)
}

func (MCPOAuthStore) Delete(key string) error {
	if !strings.HasPrefix(key, "FYC_MCP_OAUTH_") {
		return fmt.Errorf("invalid MCP OAuth credential key")
	}
	lock, err := store.Lock(AuthFilePath() + ".lock")
	if err != nil {
		return err
	}
	defer lock.Unlock()
	auth, corrupt := loadAuthStore()
	if corrupt {
		return fmt.Errorf("auth.json is unreadable; no credentials were deleted")
	}
	if _, ok := auth[key]; !ok {
		return nil
	}
	delete(auth, key)
	return saveAuthStore(auth)
}
