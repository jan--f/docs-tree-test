package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
)

type requestIDKey struct{}

type auditRecord struct {
	Timestamp string `json:"timestamp"`
	RequestID string `json:"request_id,omitempty"`
	Source    string `json:"source,omitempty"`
	Actor     string `json:"actor,omitempty"`
	Action    string `json:"action"`
	Object    string `json:"object,omitempty"`
	Outcome   string `json:"outcome"`
}

var auditLogger = log.New(os.Stdout, "", 0)

func requestID(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey{}).(string)
	return v
}

func (s *Server) audit(r *http.Request, actor, action, object, outcome string) {
	record := auditRecord{Timestamp: now(), Actor: actor, Action: action, Object: object, Outcome: outcome}
	if r != nil {
		record.RequestID = requestID(r.Context())
		record.Source = s.sourceNetwork(r)
	}
	data, err := json.Marshal(record)
	if err != nil {
		log.Printf("tree-test audit marshal: %v", err)
		return
	}
	auditLogger.Print(string(data))
}

func adminFromContext(r *http.Request) adminIdentity {
	v, _ := r.Context().Value(identityKey{}).(adminIdentity)
	return v
}

func boundedAuditName(value string) string {
	if len(value) > 128 {
		return value[:128]
	}
	return value
}
