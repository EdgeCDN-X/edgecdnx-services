package edgecdnxservices

// Ready implements the ready.Readiness interface, once this flips to true CoreDNS
// assumes this plugin is ready for queries; it is not checked again.
func (e EdgeCDNXService) Ready() bool {
	for _, e := range e.InformersSynced {
		if !e() {
			return false
		}
	}
	return true
}
