package api

import (
	"errors"
	"net/http"
)

func (s *Server) handleCreateEvaluation(w http.ResponseWriter, r *http.Request) {
	request, err := DecodeEvaluationRequest(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidEvaluationRequest)
		return
	}
	response, err := NewEvaluationServiceWithPolicy(s.apiStore, EvaluationPolicy{CPUOvercommitRatio: s.cfg.API.Evaluation.CPUOvercommitRatio}).Evaluate(r.Context(), request)
	if errors.Is(err, ErrInvalidEvaluationRequest) {
		writeError(w, http.StatusBadRequest, ErrInvalidEvaluationRequest)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("evaluation failed"))
		return
	}
	writeJSON(w, http.StatusOK, response)
}
