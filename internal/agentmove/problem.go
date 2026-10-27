package agentmove

import (
	"errors"
	"fmt"
	"strings"
)

// Problem codes: why a move or copy is refused before anything is stopped.
const (
	CodeProtocolMismatch     = "protocol_mismatch"
	CodeHomeMismatch         = "home_mismatch"
	CodeUserMismatch         = "user_mismatch"
	CodeTemplateMissing      = "template_missing"
	CodeEnvironmentMissing   = "environment_missing"
	CodeHarnessMismatch      = "harness_mismatch"
	CodeHarnessBinaryMissing = "harness_binary_missing"
	CodeHarnessUnsupported   = "harness_unsupported"
	CodeSessionRootMissing   = "session_root_missing"
	CodeNameTaken            = "name_taken"
	CodeWorkspaceInUse       = "workspace_in_use"
	CodeWorkspaceExists      = "workspace_exists"
	CodeUnsafePath           = "unsafe_path"
	CodeUnsafeRef            = "unsafe_ref"
	CodeCanonicalMissing     = "canonical_missing"
	CodeCanonicalMismatch    = "canonical_mismatch"
	CodeBranchCheckedOut     = "branch_checked_out"
	CodeBranchDiverged       = "branch_diverged"
	CodeBranchExists         = "branch_exists"
	CodeGitMissing           = "git_missing"
	CodeGitUnsupported       = "git_unsupported"
	CodeGitOpInProgress      = "git_op_in_progress"
	CodeSubmoduleTorn        = "submodule_torn"
	CodeAgentMoved           = "agent_moved"
	CodeAgentBusy            = "agent_busy"
	CodeSameHost             = "same_host"
)

// Note codes: non-fatal remarks a job prints.
const (
	NoteSubmoduleURLDiffers  = "submodule_url_differs"
	NoteSubmoduleURLLocal    = "submodule_url_local"
	NoteNestedWorktreeSkip   = "nested_worktree_skipped"
	NotePersistentTaskWarned = "persistent_task"
)

// Problem is one coded refusal or note.
type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (p Problem) Error() string { return p.Code + ": " + p.Message }

func problemf(code, format string, args ...any) *Problem {
	return &Problem{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Problems is a list of refusals, as one error.
type Problems []Problem

func (ps Problems) Error() string {
	msgs := make([]string, len(ps))
	for i, p := range ps {
		msgs[i] = p.Error()
	}
	return strings.Join(msgs, "; ")
}

// HasCode reports whether err is or holds a Problem with code.
func HasCode(err error, code string) bool {
	var p *Problem
	if errors.As(err, &p) && p.Code == code {
		return true
	}
	var ps Problems
	if errors.As(err, &ps) {
		for _, q := range ps {
			if q.Code == code {
				return true
			}
		}
	}
	return false
}
