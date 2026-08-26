package server

import (
	"houdry/internal/routing"
)

func (s *Server) catalogPath() string {
	return s.opts.DataDir + "/model-catalog.json"
}

func (s *Server) loadCatalog() ([]routing.CatalogEntry, error) {
	return routing.EnsureCatalogFile(s.catalogPath())
}

func nodesToViews(nodes []Node) []routing.NodeView {
	out := make([]routing.NodeView, 0, len(nodes))
	for _, n := range nodes {
		v := routing.NodeView{
			NodeID:        n.NodeID,
			Host:          n.Host.Hostname,
			Status:        n.Status,
			ModelRuntimes: n.ModelRuntimes,
			Models:        n.Models,
		}
		if len(n.Resources.Static.GPUs) > 0 {
			v.VRAMTotal = n.Resources.Static.GPUs[0].MemoryTotalBytes
		} else if len(n.GPUs) > 0 {
			v.VRAMTotal = n.GPUs[0].MemoryTotalBytes
		}
		if len(n.Resources.Dynamic.GPUs) > 0 {
			v.VRAMAvailable = n.Resources.Dynamic.GPUs[0].MemoryAvailableBytes
		} else if v.VRAMTotal > 0 {
			v.VRAMAvailable = v.VRAMTotal
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) routePrompt(prompt string, preferRuntime string, requirePresent bool) routing.Decision {
	catalog, err := s.loadCatalog()
	if err != nil {
		catalog = routing.DefaultCatalog()
	}
	return routing.Route(routing.RouteRequest{
		Prompt:           prompt,
		Catalog:          catalog,
		Nodes:            nodesToViews(s.store.List()),
		PreferLoaded:     true,
		AllowPull:        !requirePresent,
		RequirePresent:   requirePresent,
		PreferredRuntime: preferRuntime,
	})
}
