package steering

import (
	"fmt"
	"strings"
)

func writeAdditionalDirs(b *strings.Builder, dirs []string, configDir string) {
	if len(dirs) == 0 {
		return
	}
	b.WriteString("## Additional directories\n\n")
	b.WriteString("Besides the workspace, the user lets agents read, write and delete files in ")
	b.WriteString("these directories with the file tools, and run shell commands with one as `cwd`. ")
	b.WriteString("Give their paths as absolute paths:\n\n")
	for _, d := range dirs[:min(len(dirs), maxGrantedDirs)] {
		fmt.Fprintf(b, "- `%s`\n", defuse(d))
	}
	if extra := len(dirs) - maxGrantedDirs; extra > 0 {
		fmt.Fprintf(b, "- … and %d more (`additional_directories` in `%s/config.json`)\n", extra, configDir)
	}
	b.WriteString("\n")
}
