package agentstore

// AppearancePatch edits a record's manual appearance one field at a time: a
// nil field is left unchanged, a pointer to "" clears it, anything else sets
// it. Values are validated by the caller.
type AppearancePatch struct {
	Emoji *string
	Color *string
}

// SetAppearance applies patch to name's manual appearance and returns the
// result (nil when neither field is set). It errors with ErrNotFound if name
// has no record.
func SetAppearance(homePath, name string, patch AppearancePatch) (*Appearance, error) {
	var out *Appearance
	err := Update(homePath, name, func(r Record) Record {
		next := Appearance{}
		if r.Appearance != nil {
			next = *r.Appearance
		}
		if patch.Emoji != nil {
			next.Emoji = *patch.Emoji
		}
		if patch.Color != nil {
			next.Color = *patch.Color
		}
		r.Appearance = nil
		if next != (Appearance{}) {
			stored, returned := next, next
			r.Appearance, out = &stored, &returned
		}
		return r
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
