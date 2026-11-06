package manifest

// VerbsAccepting returns the verbs whose accepts patterns cover mime.
func (m *Manifest) VerbsAccepting(mime string) []Verb {
	var out []Verb
	for _, v := range m.Verbs {
		for _, p := range v.Accepts {
			if matchMIME(p, mime) {
				out = append(out, v)
				break
			}
		}
	}
	return out
}
