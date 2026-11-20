package sync

// withComponents names the orrery.yaml fields that name a service's component.
func withComponents(o *Options, fields ...string) { o.ComponentFields = fields }
