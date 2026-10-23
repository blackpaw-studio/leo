package web

import (
	"errors"
	"net/url"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// The JSON PUT is a patch applied key by key: a present key replaces its
// value, null clears it, an absent key is not touched at all — not re-parsed,
// not re-trimmed, not re-split — so a write of one field cannot change another.
// Parsing and validation of the keys it does name are the HTML form's
// (schema.OverlayForm, schema.ApplyKeys, then config Validate).

// ApplyPatch is ApplyValues for a JSON body. values is the decoded
// {"values": ...} object (numbers as json.Number).
func (c *ConfigService) ApplyPatch(ifMatch string, res *configResource, name string, values map[string]any) (WriteResult, error) {
	opts := mutateOpts{IfMatch: ifMatch, Scope: validationScope{Resource: res.Key, Name: name}, Flag: res.flag(c), Item: c.itemOf(res, name)}
	return c.Mutate(opts, func(cfg *config.Config) error {
		target, ok := res.locate(cfg, name)
		if !ok {
			return errNotFound("%s %q not found", res.Noun, name)
		}
		if err := patchTarget(cfg, res, target, values); err != nil {
			return err
		}
		res.put(cfg, name, target)
		return nil
	})
}

// patchTarget applies a patch to target: flat fields, each subsection's
// object, then harness_options once a harness change among the flat fields is
// visible. Unknown keys are a 400, bad values a 422.
func patchTarget(cfg *config.Config, res *configResource, target any, values map[string]any) error {
	subs := map[string]configSub{}
	for _, sub := range res.Subs {
		subs[sub.Key] = sub
	}
	flat := map[string]any{}
	nested := map[string]map[string]any{}
	var problems []schema.Problem
	var unknown []string
	for key, v := range values {
		switch {
		case key == "harness_options" && res.HarnessOptions:
		case subs[key].Key != "":
			obj, ok := v.(map[string]any)
			if !ok {
				problems = append(problems, schema.Problem{Field: key, Message: "must be an object"})
				continue
			}
			nested[key] = obj
		default:
			flat[key] = v
		}
	}

	_, harnessBefore, _ := harnessView(target, cfg)
	problems, unknown = patchSection(target, res.Section, flat, "", problems, unknown)
	for _, sub := range res.Subs {
		if obj, ok := nested[sub.Key]; ok {
			problems, unknown = patchSection(sub.of(target), sub.Section, obj, sub.Key+".", problems, unknown)
		}
	}
	if res.HarnessOptions {
		patch, has := values["harness_options"]
		own, harnessAfter, _ := harnessView(target, cfg)
		if has || harnessAfter != harnessBefore {
			// Options belong to a harness: a switch drops the old harness's
			// (as the browser form does) and keeps what this write sets for
			// the new one.
			if harnessAfter != harnessBefore {
				own = nil
			}
			opts, ps := patchHarnessOptions(harnessAfter, own, patch, has)
			problems = append(problems, ps...)
			if len(ps) == 0 {
				res.setHarnessOptions(target, opts)
			}
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return errBadRequest("unknown field %s", strings.Join(unknown, ", "))
	}
	if len(problems) > 0 {
		return errInvalidFields(problems)
	}
	return nil
}

// patchSection applies obj's keys to one section of target: env-map fields by
// entry (schema.ApplyEnvPatch), everything else through the form parser for
// just those keys.
func patchSection(target any, section schema.Section, obj map[string]any, prefix string,
	problems []schema.Problem, unknown []string) ([]schema.Problem, []string) {
	plain := map[string]any{}
	for key, v := range obj {
		if kind, ok := schema.KindOf(section, key); ok && kind == schema.KindEnvMap {
			if err := schema.ApplyEnvPatch(target, section, key, v); err != nil {
				problems = append(problems, problemOf(err, prefix, key))
			}
			continue
		}
		plain[key] = v
	}
	form, ps := schema.OverlayForm(section, url.Values{}, plain)
	problems, unknown = collect(problems, unknown, ps, prefix)
	if len(ps) > 0 {
		return problems, unknown
	}
	keys := make([]string, 0, len(plain))
	for key := range plain {
		keys = append(keys, key)
	}
	if err := schema.ApplyKeys(target, section, form, keys); err != nil {
		problems = append(problems, problemOf(err, prefix, ""))
	}
	return problems, unknown
}

// problemOf pins a parse error to its field (fallback when it carries none).
func problemOf(err error, prefix, fallback string) schema.Problem {
	var fe *schema.FieldError
	if errors.As(err, &fe) {
		return schema.Problem{Field: prefix + fe.Key, Message: err.Error()}
	}
	return schema.Problem{Field: prefix + fallback, Message: err.Error()}
}

// patchHarnessOptions merges a harness_options patch into own for harnessName.
// A null patch (has && patch == nil) clears every option; otherwise each key
// sent replaces (null clears just that key) and the rest of own is kept.
func patchHarnessOptions(harnessName string, own map[string]any, patch any, has bool) (map[string]any, []schema.Problem) {
	h, err := harness.Get(harnessName)
	if err != nil {
		return nil, []schema.Problem{{Field: "harness", Message: "harness " + harnessName + " is not registered"}}
	}
	var obj map[string]any
	switch p := patch.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		obj = p
	default:
		return nil, []schema.Problem{{Field: "harness_options", Message: "must be an object"}}
	}
	form, problems := schema.OverlayHarnessForm(h, url.Values{}, obj)
	if len(problems) > 0 {
		return nil, problems
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	opts, err := schema.ApplyHarnessOptionKeys(h, own, form, keys)
	if err != nil {
		return nil, []schema.Problem{problemOf(err, "", "harness_options")}
	}
	return opts, nil
}

// collect splits overlay problems into value problems (422) and unknown keys
// (400), prefixing nested ones with their subsection key.
func collect(problems []schema.Problem, unknown []string, ps []schema.Problem, prefix string) ([]schema.Problem, []string) {
	for _, p := range ps {
		if p.Unknown {
			unknown = append(unknown, prefix+p.Field)
			continue
		}
		p.Field = prefix + p.Field
		problems = append(problems, p)
	}
	return problems, unknown
}
