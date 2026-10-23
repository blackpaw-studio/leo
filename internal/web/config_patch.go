package web

import (
	"net/url"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// The JSON PUT is a patch over the form a browser would POST for the item's
// current state: a present key replaces its value, null clears it, an absent
// key keeps it. The patched form then goes through the same applyForm as the
// HTML save, so parsing, cascade and validation have one implementation.

// ApplyPatch is ApplyValues for a JSON body. values is the decoded
// {"values": ...} object (numbers as json.Number).
func (c *ConfigService) ApplyPatch(ifMatch string, res *configResource, name string, values map[string]any) (WriteResult, error) {
	opts := mutateOpts{IfMatch: ifMatch, Scope: validationScope{Resource: res.Key, Name: name}, Flag: res.flag(c)}
	return c.Mutate(opts, func(cfg *config.Config) error {
		target, ok := res.locate(cfg, name)
		if !ok {
			return errNotFound("%s %q not found", res.Noun, name)
		}
		form, err := patchedForm(cfg, res, target, values)
		if err != nil {
			return err
		}
		harnessPatch, hasHarness := values["harness_options"]
		check := func(cfg *config.Config, target any, form url.Values) []schema.Problem {
			if !hasHarness {
				return nil
			}
			return overlayHarnessOptions(cfg, target, form, harnessPatch)
		}
		if err := applyForm(cfg, res, target, form, check); err != nil {
			return err
		}
		res.put(cfg, name, target)
		return nil
	})
}

// patchedForm overlays values onto the item's current form: flat fields, then
// each subsection's object. harness_options is overlaid later, once the flat
// fields (a harness change among them) have been applied.
func patchedForm(cfg *config.Config, res *configResource, target any, values map[string]any) (url.Values, error) {
	subs := map[string]configSub{}
	for _, sub := range res.Subs {
		subs[sub.Key] = sub
	}
	flat := map[string]any{}
	nested := map[string]any{}
	for key, v := range values {
		switch {
		case key == "harness_options" && res.HarnessOptions:
		case subs[key].Key != "":
			nested[key] = v
		default:
			flat[key] = v
		}
	}

	form := baseForm(cfg, res, target)
	var problems []schema.Problem
	var unknown []string

	form, ps := schema.OverlayForm(res.Section, form, flat)
	problems, unknown = collect(problems, unknown, ps, "")
	for _, sub := range res.Subs {
		raw, ok := nested[sub.Key]
		if !ok {
			continue
		}
		obj, ok := raw.(map[string]any)
		if !ok {
			problems = append(problems, schema.Problem{Field: sub.Key, Message: "must be an object"})
			continue
		}
		form, ps = schema.OverlayForm(sub.Section, form, obj)
		problems, unknown = collect(problems, unknown, ps, sub.Key+".")
	}
	if len(unknown) > 0 {
		return nil, errBadRequest("unknown field %s", strings.Join(unknown, ", "))
	}
	if len(problems) > 0 {
		return nil, errInvalidFields(problems)
	}
	return form, nil
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

// overlayHarnessOptions overlays a harness_options patch onto form for the
// scope's effective harness (already reflecting a harness change in the same
// write). null clears every option; a key the harness lacks is a problem on
// harness_options.<key>.
func overlayHarnessOptions(cfg *config.Config, target any, form url.Values, patch any) []schema.Problem {
	_, harnessName, _ := harnessView(target, cfg)
	h, err := harness.Get(harnessName)
	if err != nil {
		return []schema.Problem{{Field: "harness", Message: "harness " + harnessName + " is not registered"}}
	}
	var obj map[string]any
	switch p := patch.(type) {
	case nil:
		obj = map[string]any{}
		for _, f := range h.OptionsSchema() {
			obj[f.Key] = nil
		}
	case map[string]any:
		obj = p
	default:
		return []schema.Problem{{Field: "harness_options", Message: "must be an object"}}
	}
	out, problems := schema.OverlayHarnessForm(h, form, obj)
	for k, v := range out {
		form[k] = v
	}
	return problems
}

// baseForm renders item's current state as the form a browser would POST:
// every flat field, each subsection's fields and the current harness options.
func baseForm(cfg *config.Config, res *configResource, target any) url.Values {
	form := url.Values{}
	renderInto(form, schema.Values(target, res.Section, nil))
	for _, sub := range res.Subs {
		renderInto(form, schema.Values(sub.of(target), sub.Section, nil))
	}
	if res.HarnessOptions {
		own, harnessName, _ := harnessView(target, cfg)
		if h, err := harness.Get(harnessName); err == nil {
			for _, fv := range schema.HarnessOptionValues(h, own, nil, schema.OptionSources{Cfg: cfg}) {
				form.Set(fv.InputName, fv.Value)
			}
		}
	}
	return form
}

func renderInto(form url.Values, fvs []schema.FieldValue) {
	for _, fv := range fvs {
		if fv.Kind == schema.KindBool {
			form.Set(fv.Key, boolForm(fv.Checked))
			continue
		}
		form.Set(fv.Key, fv.Value)
	}
}

func boolForm(on bool) string {
	if on {
		return "true"
	}
	return "false"
}
