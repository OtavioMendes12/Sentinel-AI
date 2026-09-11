package domain

// PullRequest is the pull request under review. Title and Body are written
// by the pull request author and must be treated as untrusted.
type PullRequest struct {
	Repository string // "owner/name"
	Number     int
	Title      string
	Body       string
	State      string // "open" or "closed"
	Draft      bool
	BaseSHA    string
	HeadSHA    string
}

// ChangedFile is one file changed by a pull request.
type ChangedFile struct {
	Path         string
	PreviousPath string // set for renamed files
	Status       string // added, removed, modified, renamed, copied, changed
	Additions    int
	Deletions    int
	Patch        string // unified diff hunks; empty for binary or very large files
}
