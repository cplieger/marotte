package forges

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// credentialStoreDir is the library's credential store under the config
// directory. The directory is the library's custody; nothing else writes in it.
const credentialStoreDir = "forge-store"

// openCredentialStore opens the store under configDir. A store that cannot be
// opened is not a boot failure: it answers nil and the reason every connection
// row carries until the directory is repaired. It logs nothing; the caller logs
// the reason once per distinct value.
func openCredentialStore(configDir string) (store *creds.FileStore, degraded string) {
	dir, err := filepath.Abs(filepath.Join(configDir, credentialStoreDir))
	if err != nil {
		dir = filepath.Join(configDir, credentialStoreDir)
	}
	store, err = creds.OpenFileStore(dir, forgeapi.WithLogger(slog.Default().With("component", "forges")))
	if err != nil {
		return nil, fmt.Sprintf("the forge credential store %s cannot be used (%v). It must be a directory this user owns at mode 0700", dir, err)
	}
	return store, ""
}
