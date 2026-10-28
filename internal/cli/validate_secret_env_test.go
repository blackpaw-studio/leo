package cli

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/secretenv"
)

const validateSentinel = "S3CR3T-SENTINEL"

// serviceResolver answers by keychain service: OK resolves to the
// sentinel, AWAY is unavailable here, LEAKY fails with the sentinel in a
// non-ResolutionError, anything else is not found. It counts calls.
type serviceResolver struct {
	mu    sync.Mutex
	calls map[string]int
}

func (r *serviceResolver) Resolve(ref secretenv.Ref) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[ref.Service]++
	switch ref.Service {
	case "OK":
		return validateSentinel, nil
	case "AWAY":
		return "", &secretenv.ResolutionError{Ref: ref, Reason: "not supported on linux", Unavailable: true}
	case "LEAKY":
		return "", errors.New("backend said " + validateSentinel)
	}
	return "", &secretenv.ResolutionError{Ref: ref, Reason: "item not found"}
}

func svc(service string) config.EnvValue {
	return config.Secret(secretenv.Ref{Backend: secretenv.Keychain, Service: service})
}

func secretValidateConfig() *config.Config {
	return &config.Config{
		Environments: map[string]config.EnvMap{
			"a": {"OK1": svc("OK"), "GONE": svc("GONE"), "LEAK": svc("LEAKY")},
			"b": {"OK2": svc("OK"), "AWAY": svc("AWAY")},
		},
		Templates: map[string]config.TemplateConfig{
			"t": {Environments: []string{}, Env: config.EnvMap{"OK3": svc("OK"), "LIT": config.Literal("x")}},
		},
	}
}

func TestSecretFindingsClassifyAndDedupe(t *testing.T) {
	r := &serviceResolver{calls: map[string]int{}}
	got := secretFindings(secretValidateConfig(), config.Expander{Secrets: r})
	want := []Finding{
		{Severity: SeverityError, Check: "secrets", Message: "environments.a.GONE: secret keychain(service=GONE): item not found"},
		{Severity: SeverityError, Check: "secrets", Message: "environments.a.LEAK: secret keychain(service=LEAKY): resolution failed"},
		{Severity: SeverityWarn, Check: "secrets", Message: "environments.b.AWAY: secret keychain(service=AWAY): not supported on linux"},
		{Severity: SeverityInfo, Check: "secrets", Message: "3 references resolved"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("findings =\n%+v\nwant\n%+v", got, want)
	}
	if want := map[string]int{"OK": 1, "GONE": 1, "LEAKY": 1, "AWAY": 1}; !reflect.DeepEqual(r.calls, want) {
		t.Errorf("resolver calls = %v, want one per distinct reference %v", r.calls, want)
	}
}

func TestSecretFindingsNoneWithoutRefs(t *testing.T) {
	cfg := &config.Config{Environments: map[string]config.EnvMap{"a": {"X": config.Literal("1")}}}
	if got := secretFindings(cfg, config.Expander{Secrets: fatalResolver{t}}); len(got) != 0 {
		t.Errorf("findings = %+v", got)
	}
}

func TestValidateOutputNeverCarriesSecretValues(t *testing.T) {
	findings := secretFindings(secretValidateConfig(), config.Expander{Secrets: &serviceResolver{calls: map[string]int{}}})
	text := captureColorOutput(t, func() {
		_ = captureStdoutForConfigTests(t, func() { _ = emitValidateText(findings, nil) })
	})
	js := captureStdoutForConfigTests(t, func() { _ = emitValidateJSON(findings) })
	for name, out := range map[string]string{"text": text, "json": js} {
		if !strings.Contains(out, "service=GONE") {
			t.Errorf("%s output lacks the findings:\n%s", name, out)
		}
		if strings.Contains(out, validateSentinel) {
			t.Errorf("%s output carries the secret value:\n%s", name, out)
		}
	}
}
