package bridge

import (
	"bytes"
	"encoding/json"
)

// TurnTokens is what one main-loop turn cost, as the mod reports it on the
// turn's turn.complete: the turn's own token counts (not the session's), and
// the model that answered ("" when nothing counted).
type TurnTokens struct {
	Input         int64
	Output        int64
	CacheRead     int64
	CacheCreation int64
	Model         string
}

// wireTokens is TurnTokens as the mod sends it. The counts are pointers so
// a missing one is told from a zero.
type wireTokens struct {
	Input         *int64  `json:"input"`
	Output        *int64  `json:"output"`
	CacheRead     *int64  `json:"cache_read"`
	CacheCreation *int64  `json:"cache_creation"`
	Model         *string `json:"model"`
}

// parseTokens strictly decodes a turn.complete's tokens: an object with all
// four counts as non-negative integers, an optional string model, nothing
// else. Absent tokens are nil; null or anything malformed is refused.
func parseTokens(fields map[string]json.RawMessage) (*TurnTokens, error) {
	raw, present := fields["tokens"]
	if !present {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var w wireTokens
	if isNull(raw) || dec.Decode(&w) != nil {
		return nil, invalidReport("tokens must be an object of integer counts")
	}
	counts := []*int64{w.Input, w.Output, w.CacheRead, w.CacheCreation}
	for _, c := range counts {
		if c == nil || *c < 0 {
			return nil, invalidReport("tokens needs input, output, cache_read and cache_creation, each a count of zero or more")
		}
	}
	tokens := &TurnTokens{Input: *w.Input, Output: *w.Output, CacheRead: *w.CacheRead, CacheCreation: *w.CacheCreation}
	if w.Model != nil {
		tokens.Model = *w.Model
	}
	return tokens, nil
}

func cloneTokens(t *TurnTokens) *TurnTokens {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}
