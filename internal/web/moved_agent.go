package web

// codeAgentMoved is the error code of a request refused because its agent
// moved to another host; it matches the daemon socket's.
const codeAgentMoved = "agent_moved"

// movedReporter is the part of an AgentService (agent.Manager) that knows
// which agents moved to another host.
type movedReporter interface {
	MovedErr(name string) error
}

// movedErr returns the refusal for an agent moved to another host (an
// *agent.MovedError naming it), nil otherwise.
func (s *Server) movedErr(name string) error {
	r, ok := s.agentSvc.(movedReporter)
	if !ok {
		return nil
	}
	return r.MovedErr(name)
}
