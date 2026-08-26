package server

// tryScheduleQueued assigns queued jobs to READY nodes that Fit requirements.
// Selection prefers model locality (LOADED > AVAILABLE > pull), then stable node_id.
func (s *Server) tryScheduleQueued() {
	queued := s.jobs.ListByStatus(JobQueued)
	if len(queued) == 0 {
		return
	}
	nodes := s.store.List()
	for _, j := range queued {
		nodeID := s.selectNode(nodes, j)
		if nodeID == "" {
			continue
		}
		_ = s.jobs.Assign(j.ID, nodeID)
	}
}

func (s *Server) selectNode(nodes []Node, j Job) string {
	var best string
	bestScore := -1
	for _, n := range nodes {
		if j.PreferredNodeID != "" && n.NodeID != j.PreferredNodeID {
			continue
		}
		if j.NodeID != "" && n.NodeID != j.NodeID {
			continue
		}
		if !Fits(n, j.Requirements) {
			continue
		}
		score := ModelScore(n, j.Requirements)
		if best == "" || score > bestScore || (score == bestScore && n.NodeID < best) {
			best = n.NodeID
			bestScore = score
		}
	}
	return best
}
