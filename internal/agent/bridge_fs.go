// File-system handlers for kiro-cli's fs/read_text_file and fs/write_text_file: every path
// is confined to WORKDIR (https://agentclientprotocol.com/protocol/file-system). Handlers
// run on their own goroutine so the forward loop keeps streaming.

package agent
