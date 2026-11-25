package agentstore

import "github.com/blackpaw-studio/leo/internal/brief"

// SetBrief stores b as name's brief, or clears it when b is nil. It errors
// with ErrNotFound if name has no record.
func SetBrief(homePath, name string, b *brief.Brief) error {
	return Update(homePath, name, func(r Record) Record {
		r.Brief = b.Clone()
		return r
	})
}

// BriefOf is name's stored brief, nil when it has none or no record.
func BriefOf(homePath, name string) *brief.Brief {
	records, err := Load(FilePath(homePath))
	if err != nil {
		return nil
	}
	return records[name].Brief
}
