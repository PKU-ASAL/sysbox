package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func (s *Server) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := s.apiStore.ListWorkers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	workers = ensureLocalWorker(workers)
	sort.Slice(workers, func(i, j int) bool { return workers[i].ID < workers[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"workers": workers})
}

func (s *Server) handleRegisterWorker(w http.ResponseWriter, r *http.Request) {
	var req controlplane.Worker
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode worker: %w", err))
		return
	}
	worker, err := normalizeWorker(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.apiStore.SaveWorker(r.Context(), worker); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, worker)
}

func (s *Server) handleGetWorker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("worker")
	if err := validatePathSegment(id, "worker"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if id == DefaultWorkerID {
		writeJSON(w, http.StatusOK, localWorker())
		return
	}
	worker, err := s.apiStore.GetWorker(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

func (s *Server) handleListWorkerRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("worker")
	if err := validatePathSegment(id, "worker"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": s.assignedRunsForWorker(r.Context(), id)})
}

func (s *Server) handleClaimWorkerRun(w http.ResponseWriter, r *http.Request) {
	workerID := r.PathValue("worker")
	runID := r.PathValue("id")
	if err := validatePathSegment(workerID, "worker"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validatePathSegment(runID, "id"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, err := s.jobs.claim(runID, workerID)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleWorkerHeartbeat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("worker")
	if err := validatePathSegment(id, "worker"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req controlplane.Worker
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, fmt.Errorf("decode worker heartbeat: %w", err))
			return
		}
	}
	req.ID = id
	req.Status = "online"
	worker, err := normalizeWorker(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if existing, err := s.apiStore.GetWorker(r.Context(), id); err == nil && existing != nil {
		if worker.Name == "" {
			worker.Name = existing.Name
		}
		if len(worker.Capabilities) == 0 {
			worker.Capabilities = existing.Capabilities
		}
		if len(worker.Labels) == 0 {
			worker.Labels = existing.Labels
		}
		if worker.Version == "" {
			worker.Version = existing.Version
		}
		worker.CreatedAt = existing.CreatedAt
	}
	worker.LastHeartbeat = time.Now().UTC()
	worker.UpdatedAt = worker.LastHeartbeat
	if err := s.apiStore.SaveWorker(r.Context(), worker); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

func normalizeWorker(in controlplane.Worker) (controlplane.Worker, error) {
	if in.ID == "" {
		in.ID = in.Name
	}
	if err := validatePathSegment(in.ID, "worker"); err != nil {
		return controlplane.Worker{}, err
	}
	now := time.Now().UTC()
	if in.Name == "" {
		in.Name = in.ID
	}
	if in.Status == "" {
		in.Status = "online"
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = now
	}
	if in.Status == "online" && in.LastHeartbeat.IsZero() {
		in.LastHeartbeat = now
	}
	return in, nil
}

func (s *Server) assignedRunsForWorker(ctx context.Context, workerID string) []Run {
	runs, err := s.apiStore.LoadRuns(ctx)
	if err != nil {
		return nil
	}
	out := make([]Run, 0)
	for _, run := range latestRunsByID(runs) {
		normalizeRunProductFields(&run)
		if run.WorkerID == workerID && run.Status == RunAssigned {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QueuedAt.Before(out[j].QueuedAt) })
	return out
}

func ensureLocalWorker(workers []controlplane.Worker) []controlplane.Worker {
	for _, worker := range workers {
		if worker.ID == DefaultWorkerID {
			return workers
		}
	}
	return append(workers, localWorker())
}

func localWorker() controlplane.Worker {
	now := time.Now().UTC()
	return controlplane.Worker{
		ID:            DefaultWorkerID,
		Name:          "local API worker",
		Status:        "online",
		Capabilities:  []string{"docker", "network", "firecracker", "libvirt"},
		Labels:        map[string]string{"execution": "in-process"},
		LastHeartbeat: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}
