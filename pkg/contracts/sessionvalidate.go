package contracts

import "fmt"

// SessionValidation is the answer to POST /sessions/validate, the dry run of
// POST /sessions: what the create would resolve to, and every reason it
// would be refused. Nothing is created to produce it.
type SessionValidation struct {
	// Dir and Isolation are the resolved values — after defaults, and after
	// joining a linked counterpart's worktree (Isolation "current_dir").
	Dir       string `json:"dir"`
	Isolation string `json:"isolation"`
	// Workspace is the resolved workspace ID, empty when none applies.
	Workspace string `json:"workspace,omitempty"`
	// JoinedWorktreeOf names the counterpart whose isolated copy the new
	// session would work in, when staged links imply a join.
	JoinedWorktreeOf *JoinedWorktree `json:"joined_worktree_of,omitempty"`
	// Errors are the blocking problems, in ValidationError's field shape;
	// always present, empty when the create would be accepted.
	Errors []FieldError `json:"errors"`
}

// JoinedWorktree identifies the session a new one would share a worktree with.
type JoinedWorktree struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
}

// ValidateSession is the Data Service's check on a session record, run on
// every create and update (422 on failure): a name, and every enum field
// either empty or one it knows. Shared with the Router's POST
// /sessions/validate dry run so the two can't disagree.
func ValidateSession(s *Session) *ValidationError {
	var v ValidationError
	if s.Name == "" {
		v.Add("name", "required")
	}
	if s.Activity != "" && s.Activity != SessionActive && s.Activity != SessionInactive {
		v.Add("activity", fmt.Sprintf("must be %q or %q", SessionActive, SessionInactive))
	}
	if s.Isolation != "" && s.Isolation != IsolationWorktree && s.Isolation != IsolationCurrentDir {
		v.Add("isolation", fmt.Sprintf("must be %q or %q", IsolationWorktree, IsolationCurrentDir))
	}
	if s.NameSource != "" && s.NameSource != SessionNameSourceDerived &&
		s.NameSource != SessionNameSourceUser && s.NameSource != SessionNameSourceGenerated {
		v.Add("name_source", fmt.Sprintf("must be %q, %q, or %q",
			SessionNameSourceDerived, SessionNameSourceUser, SessionNameSourceGenerated))
	}
	if s.SynthesisMode != "" && !s.SynthesisMode.Valid() {
		v.Add("synthesis_mode", fmt.Sprintf("unknown synthesis mode %q", s.SynthesisMode))
	}
	// Fail closed: an unrecognized trust value would otherwise fall through
	// trustArgs' default branch at spawn (providers/claudecode) and run with
	// FULL access — a typo'd "read-only" must be rejected here, not silently
	// granted everything.
	if s.Trust != "" && !s.Trust.Valid() {
		v.Add("trust", "must be one of: "+SessionTrustChoices())
	}
	if v.Empty() {
		return nil
	}
	return &v
}
