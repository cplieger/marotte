package marotte

// DefaultUploadDir is where a composer upload lands when the client sends no "dir": one folder at
// the container root, like an OS Downloads folder. A literal because the file handler has no notion
// of "the workspace" mount, and the client sends the same string (TestUploadPolicyMatchesClient).
// Here because filebrowse, composition and command all need it and all import this package.
const DefaultUploadDir = "/uploads"
