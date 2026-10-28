package cli

import (
	"errors"
	"fmt"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/secretenv"
)

// secretFindings resolves every secret reference in cfg's effective env
// (config.SecretReferences) through x, each distinct reference once. A
// failure is an ERROR naming the scope, key and reference; a backend that
// cannot run here (another platform's store, systemd-creds outside its
// unit) is a WARN. No finding carries a value.
func secretFindings(cfg *config.Config, x config.Expander) []Finding {
	refs := cfg.SecretReferences()
	if len(refs) == 0 {
		return nil
	}
	next := x.Secrets
	if next == nil {
		next = secretenv.System{}
	}
	x.Secrets = &onceResolver{next: next, outcomes: map[string]error{}}

	var findings []Finding
	resolved := 0
	for _, sr := range refs {
		err := x.CheckSecret(sr)
		if err == nil {
			resolved++
			continue
		}
		sev := SeverityError
		var se *config.SecretResolutionError
		if errors.As(err, &se) && se.Unavailable {
			sev = SeverityWarn
		}
		findings = append(findings, Finding{Severity: sev, Check: "secrets", Message: err.Error()})
	}
	if resolved > 0 {
		findings = append(findings, Finding{Severity: SeverityInfo, Check: "secrets", Message: pluralize(resolved, "reference") + " resolved"})
	}
	return findings
}

// onceResolver resolves each distinct reference once per validate run. It
// keeps only each outcome's error, never a value, so it suits checking and
// nothing else: a repeat lookup returns "".
type onceResolver struct {
	next     config.SecretResolver
	outcomes map[string]error
}

func (o *onceResolver) Resolve(ref secretenv.Ref) (string, error) {
	id := fmt.Sprintf("%#v", ref) // fmt sorts map keys, so equal refs match
	if err, done := o.outcomes[id]; done {
		return "", err
	}
	_, err := o.next.Resolve(ref)
	o.outcomes[id] = err
	return "", err
}
