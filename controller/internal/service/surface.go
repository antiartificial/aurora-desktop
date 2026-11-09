package service

import (
	"github.com/antiartificial/aurora/contracts/manifest"
	"github.com/antiartificial/aurora/contracts/surface"
)

// surfaceEffect maps a manifest effect onto the surface contract's three
// classes. The manifest says more than the surface needs to know: destructive
// and external are both things a person must be told about, which is exactly
// what surface.EffectWrite means, so they map up rather than staying distinct.
// Anything else maps to "" and is left for Validate to refuse -- a default of
// "nominal" here would turn an unpoliced effect into one the shell draws as
// harmless, which is the failure the contract exists to prevent.
func surfaceEffect(e manifest.Effect) string {
	switch e {
	case manifest.EffectRead:
		return surface.EffectRead
	case manifest.EffectWrite, manifest.EffectDestructive, manifest.EffectExternal:
		return surface.EffectWrite
	}
	return ""
}

// windowSurface is the descriptor of one app's window: its identity, and its
// verbs as the actions the chrome offers. It reads the manifest's verbs rather
// than one verb because a Descriptor describes a whole surface, and the shell
// draws the whole set; the caller's per-verb reply fields are built separately.
//
// The action's grants are the verb's declared capability references, unchanged.
// They are what the manifest asked for, not what the controller inferred from a
// call, and the manifest validator has already held each to an app-level
// declaration.
func windowSurface(appID, title string, verbs []manifest.Verb) surface.Descriptor {
	d := surface.Descriptor{App: appID, Title: title, Actions: make([]surface.Action, 0, len(verbs))}
	for _, v := range verbs {
		d.Actions = append(d.Actions, surface.Action{
			ID:     v.Name,
			Label:  v.Name, // the verb is the header; Summary is prose, not a label
			Effect: surfaceEffect(v.Effect),
			Grants: append([]string(nil), v.Capabilities...),
		})
	}
	return d
}
