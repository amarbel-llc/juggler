package jugglerrun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/juggler/internal/alfa/jugglerloop"
)

// The transcript is stored under transcripts/<principal>/<job>.json, 0600 in a
// 0700 directory, and the ledger's digest is the digest of exactly that file.
func TestRunAgent_PersistsTheTranscriptAndTheDigestIsTheFiles(t *testing.T) {
	h := newHarness(t)
	deps := h.agentDeps(openAIResolved(scriptedOpenAI(t, openAICreateIssue, openAIEndTurn)))
	deps.TranscriptPath = h.deps.Store.TranscriptPath

	out, err := RunAgent(context.Background(), deps, AgentRequest{Brief: parsedBrief(t, "agent-1"), MoxyURL: fakeMoxy(t).URL})
	if err != nil || out.State != StateSucceeded {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	want := filepath.Join(h.deps.Store.Root, "transcripts", "agent-1", out.Job+".json")
	if out.TranscriptPath != want {
		t.Fatalf("transcript path = %q, want %q", out.TranscriptPath, want)
	}
	data, err := os.ReadFile(out.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := jugglerloop.DigestBytes(data); got != out.Ledger.TranscriptDigest || got == "" {
		t.Errorf("digest of the file %q != ledger digest %q", got, out.Ledger.TranscriptDigest)
	}
	var tr jugglerloop.Transcript
	if err := json.Unmarshal(data, &tr); err != nil || tr.Schema != 1 || len(tr.Turns) == 0 {
		t.Fatalf("the file is not a transcript: %v %+v", err, tr)
	}
	if last := tr.Turns[len(tr.Turns)-1]; last.Body.Type != jugglerloop.BodyTerminal {
		t.Errorf("last turn = %+v, want the terminal turn", last.Body.Type)
	}
	if st, err := os.Stat(out.TranscriptPath); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v (err %v), want 0600", st.Mode().Perm(), err)
	}
	if st, err := os.Stat(filepath.Dir(out.TranscriptPath)); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v (err %v), want 0700", st.Mode().Perm(), err)
	}
	if strings.HasPrefix(out.TranscriptPath, deps.AgentStateDir) && deps.AgentStateDir != "" {
		t.Errorf("the transcript must not be inside the tool-reachable AgentStateDir")
	}
}

// No file, no digest: an unbacked digest would imply verifiability that is not
// there, so the ledger drops it and says why.
func TestRunAgent_NoTranscriptNoDigest(t *testing.T) {
	cases := map[string]func(deps *AgentDeps, root string){
		"not configured": func(deps *AgentDeps, _ string) { deps.TranscriptPath = nil },
		"write fails": func(deps *AgentDeps, root string) {
			// A regular file where the principal directory should be.
			blocker := filepath.Join(root, "blocker")
			if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			deps.TranscriptPath = func(principal, job string) string { return filepath.Join(blocker, principal, job+".json") }
		},
		"empty path": func(deps *AgentDeps, _ string) { deps.TranscriptPath = func(string, string) string { return "" } },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			deps := h.agentDeps(openAIResolved(scriptedOpenAI(t, openAICreateIssue, openAIEndTurn)))
			setup(&deps, h.deps.Store.Root)

			out, err := RunAgent(context.Background(), deps, AgentRequest{Brief: parsedBrief(t, "agent-1"), MoxyURL: fakeMoxy(t).URL})
			if err != nil || out.State != StateSucceeded {
				t.Fatalf("a missing transcript must not fail the run: %+v, err = %v", out, err)
			}
			if out.TranscriptPath != "" || out.Ledger.TranscriptDigest != "" {
				t.Errorf("path = %q, digest = %q, want neither", out.TranscriptPath, out.Ledger.TranscriptDigest)
			}
			if !containsNote(out.Ledger.Notes, "transcript not persisted") {
				t.Errorf("notes = %v, want a 'transcript not persisted' note", out.Ledger.Notes)
			}
		})
	}
}

func TestStore_TranscriptPath_KeyedLikeTrafficAndRejectsUnsafeSegments(t *testing.T) {
	s := Store{Root: "/state"}
	if got, want := s.TranscriptPath("agent-1", "job-1"), "/state/transcripts/agent-1/job-1.json"; got != want {
		t.Errorf("TranscriptPath = %q, want %q", got, want)
	}
	for _, c := range [][2]string{{"../x", "job"}, {"agent", "../../etc"}, {"", "job"}, {"agent", ""}, {"a/b", "job"}} {
		if got := s.TranscriptPath(c[0], c[1]); got != "" {
			t.Errorf("TranscriptPath(%q, %q) = %q, want empty", c[0], c[1], got)
		}
	}
}

// spawn --wait reports the file's path only when it exists, like traffic.
func TestWaitChild_ReportsThePersistedTranscript(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	newRun(t, h, "rec-42")
	rec := spawnChild(t, h)
	finishAgent(t, h, rec, StateSucceeded, "evaluator passed", jugglerloop.Ledger{Schema: 1, Calls: []jugglerloop.LedgerCall{}})

	w, err := WaitChild(context.Background(), h.deps, rec, time.Second, 0)
	if err != nil || w.Transcript != "" {
		t.Fatalf("no file yet: transcript = %q, err = %v", w.Transcript, err)
	}
	p := h.deps.Store.TranscriptPath(rec.Principal, rec.Job)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err = WaitChild(context.Background(), h.deps, rec, time.Second, 0)
	if err != nil || w.Transcript != p {
		t.Fatalf("transcript = %q, want %q (err %v)", w.Transcript, p, err)
	}
}

// The retention rule: the existing resolved-run sweep removes the transcript
// with the traffic file, and its now-empty directory.
func TestClean_SweepsAResolvedRunsPersistedTranscript(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	run := &RunRecord{Schema: RecordSchema, RunKey: "k", RootPrincipal: "root-x", CreatedAt: t0, Resolved: &Resolution{State: StateSucceeded, Reason: "done", At: t0}}
	if err := h.deps.Store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	child := &ChildRecord{Schema: RecordSchema, RunKey: "k", BriefDigest: "sha256:abc", Principal: "child-1", Job: "job-1", Parent: "root-x", CreatedAt: t0}
	if err := h.deps.Store.SaveChild(child); err != nil {
		t.Fatal(err)
	}
	p := h.deps.Store.TranscriptPath(child.Principal, child.Job)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.deps.Now = func() time.Time { return t0.Add(2 * time.Hour) }
	req := CleanRequest{OlderThan: time.Hour, Resolved: true, ResolvedOlderThan: time.Hour}

	dry, err := Clean(context.Background(), h.deps, req)
	if err != nil || len(dry.Runs) != 1 || !strings.Contains(dry.Runs[0].Detail, "would remove 1") {
		t.Fatalf("dry run = %+v, err = %v", dry.Runs, err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("a dry run must keep the file: %v", err)
	}
	req.Act = true
	out, err := Clean(context.Background(), h.deps, req)
	if err != nil || len(out.Runs) != 1 || !out.Runs[0].Reaped || !strings.Contains(out.Runs[0].Detail, "removed 1 of 1") {
		t.Fatalf("clean = %+v, err = %v", out.Runs, err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("the transcript should be swept: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
		t.Errorf("the empty transcripts/<principal>/ directory should go too: %v", err)
	}
}
