package powers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
)

// maxPowerFileBytes bounds one installed Power's mcp.json read.
const maxPowerFileBytes = 1 << 20

// legacyStripped are the members KAS's own legacy-Power render drops: the
// approval lists belong to the IDE's trust prompt, which KAS does not read.
var legacyStripped = []string{"autoApprove", "allowedTools"}

// LegacyServers renders `powers.mcpServers` from the Powers installed under
// installedDir (`~/.kiro/powers/installed`): every server of every legacy Power,
// keyed `power-<power>-<server>` as KAS's resolver expects. An agent plugin
// (`dev.kiro/plugin.json`) is skipped, because KAS loads its servers itself. A
// Power whose mcp.json cannot be read, or a server key two Powers derive, is left
// out and reported as a *ScanError, so one bad directory does not drop the others'.
func LegacyServers(installedDir string) (map[string]json.RawMessage, []error) {
	out := map[string]json.RawMessage{}
	dirents, err := os.ReadDir(installedDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return out, []error{err}
	}
	var errs []error
	owners := map[string][]string{}
	for _, d := range dirents {
		name := d.Name()
		if !d.IsDir() || !ValidName(name) {
			continue
		}
		dir := filepath.Join(installedDir, name)
		if _, err := os.Lstat(filepath.Join(dir, "dev.kiro", "plugin.json")); err == nil {
			continue
		}
		servers, err := readServers(filepath.Join(dir, "mcp.json"))
		if err != nil {
			errs = append(errs, &ScanError{Power: name, Err: err})
			continue
		}
		for _, server := range sortedNames(servers) {
			key := "power-" + name + "-" + server
			owners[key] = append(owners[key], name)
			out[key] = servers[server]
		}
	}
	return out, append(errs, dropCollisions(out, owners)...)
}

// dropCollisions removes every key two Powers derive (`a-b`+`c` and `a`+`b-c` both
// give `power-a-b-c`), because keeping either would run one Power's server under
// the other's name; each such Power is reported as a *ScanError.
func dropCollisions(out map[string]json.RawMessage, owners map[string][]string) []error {
	var errs []error
	for _, key := range sortedNames(owners) {
		powers := owners[key]
		if len(powers) < 2 {
			continue
		}
		delete(out, key)
		for _, p := range powers {
			errs = append(errs, &ScanError{Power: p, Err: fmt.Errorf("server key %s is also derived by another power", key)})
		}
	}
	return errs
}

// readServers reads one mcp.json's `mcpServers`, stripping legacyStripped from
// each entry. An absent file declares no servers.
func readServers(path string) (map[string]json.RawMessage, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("mcp.json is not a regular file")
	}
	if info.Size() > maxPowerFileBytes {
		return nil, fmt.Errorf("mcp.json is over %d bytes", maxPowerFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		MCPServers map[string]map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("mcp.json: %w", err)
	}
	out := make(map[string]json.RawMessage, len(doc.MCPServers))
	for server, entry := range doc.MCPServers {
		if server == "" || entry == nil {
			continue
		}
		for _, k := range legacyStripped {
			delete(entry, k)
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		out[server] = raw
	}
	return out, nil
}

func sortedNames[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
