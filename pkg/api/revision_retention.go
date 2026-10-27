package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

type RevisionRetentionPolicy struct {
	KeepLast  int       `json:"keep_last"`
	OlderThan time.Time `json:"older_than"`
	Limit     int       `json:"limit"`
}
type RevisionRetentionEntry struct {
	Revision string `json:"revision"`
	Reason   string `json:"reason"`
}
type RevisionRetentionPlan struct {
	Policy             RevisionRetentionPolicy  `json:"policy"`
	Candidates         []string                 `json:"candidates"`
	Retained           []RevisionRetentionEntry `json:"retained"`
	Digest             string                   `json:"digest"`
	DeletionAuthorized bool                     `json:"deletion_authorized"`
	Consistency        string                   `json:"consistency"`
}

func (s *Server) planRevisionRetention(ctx context.Context, policy RevisionRetentionPolicy) (RevisionRetentionPlan, error) {
	var zero RevisionRetentionPlan
	if policy.KeepLast < 0 || policy.KeepLast > 10000 || policy.Limit < 1 || policy.Limit > 128 || policy.OlderThan.IsZero() || policy.OlderThan.After(time.Now()) {
		return zero, fmt.Errorf("keep_last 0..10000, limit 1..128 and past older_than required")
	}
	revisions, err := s.globalRevisionInventory(ctx)
	if err != nil {
		return zero, err
	}
	refs, err := s.lifecycleRevisionReferences(ctx)
	if err != nil {
		return zero, err
	}
	runs, err := s.revisionRunReferences(ctx)
	if err != nil {
		return zero, err
	}
	for revision := range runs {
		refs[revision] = true
	}
	sort.Slice(revisions, func(i, j int) bool {
		if revisions[i].CreatedAt.Equal(revisions[j].CreatedAt) {
			return revisions[i].Revision < revisions[j].Revision
		}
		return revisions[i].CreatedAt.After(revisions[j].CreatedAt)
	})
	out := RevisionRetentionPlan{Policy: policy, Candidates: []string{}, Retained: []RevisionRetentionEntry{}, Consistency: "separate registry/lifecycle/run observations; preview only"}
	for i, rev := range revisions {
		reason := ""
		switch {
		case refs[rev.Revision]:
			reason = "retained reference"
		case i < policy.KeepLast:
			reason = "keep_last"
		case !rev.CreatedAt.Before(policy.OlderThan):
			reason = "retention age"
		case len(out.Candidates) >= policy.Limit:
			reason = "batch limit"
		}
		if reason != "" {
			out.Retained = append(out.Retained, RevisionRetentionEntry{rev.Revision, reason})
		} else {
			out.Candidates = append(out.Candidates, rev.Revision)
		}
	}
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	for _, value := range []any{out, revisions, runs, refs} {
		if err = encoder.Encode(value); err != nil {
			return zero, err
		}
	}
	out.Digest = fmt.Sprintf("%x", digest.Sum(nil))
	return out, nil
}
func (s *Server) handleRevisionRetention(w http.ResponseWriter, r *http.Request) {
	var policy RevisionRetentionPolicy
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, 400, fmt.Errorf("one JSON object required"))
		return
	}
	plan, err := s.planRevisionRetention(r.Context(), policy)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 200, plan)
}
