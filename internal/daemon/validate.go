package daemon

import (
	"context"

	"connectrpc.com/connect"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/labfile"
)

// Validate checks inline YAML as `parker validate` does. A file that
// doesn't load or expand is a normal answer (ok false, with errors), not
// an RPC error.
func (s *Server) Validate(_ context.Context, req *connect.Request[parkerv1.ValidateRequest]) (*connect.Response[parkerv1.ValidateResponse], error) {
	return connect.NewResponse(ValidateYAML(req.Msg.Yaml)), nil
}

// ValidateYAML loads, expands and checks a topology given as text.
func ValidateYAML(text string) *parkerv1.ValidateResponse {
	r := labfile.Check(text, "")
	out := &parkerv1.ValidateResponse{Ok: r.OK, Warnings: r.Warnings, Networks: int32(r.Networks), Nodes: int32(r.Nodes)}
	for _, p := range r.Problems {
		out.Errors = append(out.Errors, &parkerv1.ValidateProblem{Message: p.Message, Line: int32(p.Line), Path: p.Path})
	}
	return out
}
