package web

import (
	"sort"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// schemaVersion bumps only when the schema document's shape changes
// incompatibly; additive fields do not.
const schemaVersion = 1

// namePattern is the shape every config entity name must match (the daemon
// additionally refuses "." and "..").
const namePattern = "^[A-Za-z0-9._-]+$"

type subsectionDoc struct {
	Key     string            `json:"key"`
	Section string            `json:"section"`
	Fields  []schema.FieldDoc `json:"fields"`
}

type resourceDoc struct {
	Key            string            `json:"key"`
	Label          string            `json:"label"`
	Kind           string            `json:"kind"`
	Section        string            `json:"section"`
	Actions        []string          `json:"actions"`
	HarnessOptions bool              `json:"harness_options"`
	Restart        string            `json:"restart"`
	Fields         []schema.FieldDoc `json:"fields"`
	Subsections    []subsectionDoc   `json:"subsections"`
}

type harnessDoc struct {
	Fields []schema.FieldDoc `json:"fields"`
}

type schemaDocument struct {
	SchemaVersion int                   `json:"schema_version"`
	NamePattern   string                `json:"name_pattern"`
	Resources     []resourceDoc         `json:"resources"`
	Harnesses     map[string]harnessDoc `json:"harnesses"`
}

// stampRestart records what a change needs on every field of a resource.
func stampRestart(docs []schema.FieldDoc, restart string) []schema.FieldDoc {
	for i := range docs {
		docs[i].Restart = restart
	}
	return docs
}

// buildSchemaDocument serves the registry as JSON: one entry per resource the
// web UI edits, plus the per-harness option schemas. It needs no loaded
// config, so a client can render forms for a machine whose leo.yaml is broken.
func (c *ConfigService) buildSchemaDocument() schemaDocument {
	src := schema.OptionSources{Cfg: &config.Config{}, Agents: c.agentList}
	doc := schemaDocument{SchemaVersion: schemaVersion, NamePattern: namePattern, Harnesses: map[string]harnessDoc{}}
	for _, res := range configResources {
		rd := resourceDoc{
			Key: res.Key, Label: res.Label, Kind: "singleton", Section: string(res.Section),
			Actions: append([]string{}, res.Actions...), HarnessOptions: res.HarnessOptions, Restart: res.Restart,
			Fields:      stampRestart(schema.FieldDocs(res.Section, src), res.Restart),
			Subsections: []subsectionDoc{},
		}
		if res.Collection {
			rd.Kind = "collection"
		}
		for _, sub := range res.Subs {
			rd.Subsections = append(rd.Subsections, subsectionDoc{
				Key: sub.Key, Section: string(sub.Section),
				Fields: stampRestart(schema.FieldDocs(sub.Section, src), res.Restart),
			})
		}
		doc.Resources = append(doc.Resources, rd)
	}
	doc.Resources = append(doc.Resources, resourceDoc{
		Key: "delegation", Label: "Delegation", Kind: "bespoke", Actions: []string{}, Restart: restartNone,
		Fields: []schema.FieldDoc{}, Subsections: []subsectionDoc{},
	})
	for _, name := range harness.Names() {
		h, err := harness.Get(name)
		if err != nil {
			continue
		}
		doc.Harnesses[name] = harnessDoc{Fields: schema.HarnessDocs(h, src)}
	}
	return doc
}

// itemDocument is one item's values as the JSON API reads them.
type itemDocument struct {
	Name             string         `json:"name,omitempty"`
	Values           map[string]any `json:"values"`
	Inherited        any            `json:"inherited,omitempty"`
	EffectiveHarness string         `json:"effective_harness,omitempty"`
}

// buildItemDocument reads target (a pointer to the resource's struct) in JSON
// types: flat fields, subsections nested under their key, harness_options
// holding only the keys set on this scope, and the defaults-cascade
// placeholders for resources that inherit.
func buildItemDocument(cfg *config.Config, res *configResource, name string, target any) itemDocument {
	doc := itemDocument{Name: name, Values: schema.ValuesJSON(target, res.Section, nil)}
	for _, sub := range res.Subs {
		doc.Values[sub.Key] = schema.ValuesJSON(sub.of(target), sub.Section, nil)
	}
	var inherited map[string]any
	if res.Cascades {
		inherited = schema.InheritedJSON(target, res.Section, &cfg.Defaults)
	}
	if res.HarnessOptions {
		own, harnessName, inh := harnessView(target, cfg)
		doc.EffectiveHarness = harnessName
		doc.Values["harness_options"] = map[string]any{}
		if h, err := harness.Get(harnessName); err == nil {
			doc.Values["harness_options"] = schema.HarnessValuesJSON(h, own)
			if res.Cascades {
				if hi := schema.HarnessInheritedJSON(h, inh); len(hi) > 0 {
					inherited["harness_options"] = hi
				}
			}
		}
	}
	if res.Cascades {
		doc.Inherited = inherited
	}
	return doc
}

type resourceDocument struct {
	Revision string `json:"revision"`
	itemDocument
}

type collectionDocument struct {
	Revision string         `json:"revision"`
	Items    []itemDocument `json:"items"`
}

// readResource loads leo.yaml with its revision under the write lock (a writer
// is never observed half-way) and builds the resource's document: the whole
// collection, one item, or the singleton.
func (c *ConfigService) readResource(res *configResource, name string, wantItem bool) (any, string, error) {
	cfg, rev, err := c.loadLocked()
	if err != nil {
		return nil, "", err
	}
	if res.Collection && !wantItem {
		items := make([]itemDocument, 0)
		for _, n := range res.names(cfg) {
			target, _ := res.locate(cfg, n)
			items = append(items, buildItemDocument(cfg, res, n, target))
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		return collectionDocument{Revision: rev, Items: items}, rev, nil
	}
	target, ok := res.locate(cfg, name)
	if !ok {
		return nil, "", errNotFound("%s %q not found", res.Noun, name)
	}
	return resourceDocument{Revision: rev, itemDocument: buildItemDocument(cfg, res, name, target)}, rev, nil
}

// loadLocked loads leo.yaml and its revision under the write lock.
func (c *ConfigService) loadLocked() (*config.Config, string, error) {
	defer c.lockConfigWrite()()
	cfg, rev, err := config.LoadWithRevision(c.configPath)
	if err != nil {
		return nil, "", configUnavailable(err)
	}
	return cfg, rev, nil
}
