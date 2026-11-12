package jugglerrun

import (
	"fmt"

	"code.linenisgreat.com/juggler/internal/alfa/jugglerloop"
)

// persistTranscript stores the run's transcript (FDR 0023, open question 8) at
// AgentDeps.TranscriptPath(principal, job) and returns that path. The bytes are
// jugglerloop's persisted form, so Ledger.TranscriptDigest is the digest of the
// file. The file is written by writeFileAtomic: a 0700 directory, a 0600 file
// (os.CreateTemp's mode), a rename so a reader never sees a partial one.
//
// When there is no file (no path configured, an unsafe id, or a failed write)
// the digest is removed from the ledger and a note says why: a digest nothing
// backs would imply a verifiability that is not there.
func (a *agentRun) persistTranscript(res *jugglerloop.Result) string {
	drop := func(why string) string {
		res.Ledger.TranscriptDigest = ""
		res.Ledger.Notes = append(res.Ledger.Notes, "transcript not persisted: "+why)
		a.logf("transcript not persisted: %s", why)
		return ""
	}
	if len(res.TranscriptJSON) == 0 {
		return drop("the loop produced no transcript")
	}
	if a.deps.TranscriptPath == nil {
		return drop("no transcript path is configured")
	}
	if !idPattern.MatchString(a.brief.Principal) || !idPattern.MatchString(a.job) {
		return drop(fmt.Sprintf("job id %q or principal %q is not a safe path segment", a.job, a.brief.Principal))
	}
	path := a.deps.TranscriptPath(a.brief.Principal, a.job)
	if path == "" {
		return drop("no transcript path for this principal and job")
	}
	if err := writeFileAtomic(path, res.TranscriptJSON); err != nil {
		return drop(err.Error())
	}
	return path
}
