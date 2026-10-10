// File-system handlers for kiro-cli's fs/read_text_file and fs/write_text_file: a write is
// confined to WORKDIR, a read also reaches the uploads folder
// (https://agentclientprotocol.com/protocol/file-system). Handlers
// run on their own goroutine so the forward loop keeps streaming.

package agent
