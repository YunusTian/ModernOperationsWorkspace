package command

import (
	"context"
	"encoding/json"

	"github.com/mow/mow/sdk"
)

// invocationStream binds the request context resolved by Engine.RunStream to
// the transport stream. The request is authoritative: middleware may resolve
// its TargetID or confirm a dangerous operation after the caller constructed
// the original stream.
type invocationStream struct {
	sdk.Stream
	ctx        context.Context
	auditID    string
	caller     sdk.Caller
	confirmed  bool
	params     json.RawMessage
	connection *sdk.Connection
}

func (s *invocationStream) Context() context.Context    { return s.ctx }
func (s *invocationStream) AuditID() string             { return s.auditID }
func (s *invocationStream) Caller() sdk.Caller          { return s.caller }
func (s *invocationStream) Confirmed() bool             { return s.confirmed }
func (s *invocationStream) RawParams() json.RawMessage  { return s.params }
func (s *invocationStream) Connection() *sdk.Connection { return s.connection }
func (s *invocationStream) Params(dst any) error        { return json.Unmarshal(s.params, dst) }
