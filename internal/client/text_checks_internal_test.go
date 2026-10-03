package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// walkJSON inspects every occurrence of every string and number, member
// names included and unescaped, and finds a member name an object repeats at
// any depth; the same name in different objects is not a repetition.
func TestWalkJSON(t *testing.T) {
	for name, tc := range map[string]struct {
		document string
		texts    []string
		repeated bool
		ok       bool
	}{
		"plain":                  {`{"a":"x","b":[1,{"c":"y"}]}`, []string{"a", "x", "b", "1", "c", "y"}, false, true},
		"repeated member":        {`{"kid":"first","kid":"second"}`, []string{"kid", "first", "kid", "second"}, true, true},
		"escaped repeat":         {`{"kid":"first","kid":"second"}`, []string{"kid", "first", "kid", "second"}, true, true},
		"nested repeat":          {`[{"a":{"b":1,"b":2}}]`, []string{"a", "b", "1", "b", "2"}, true, true},
		"same name, two objects": {`[{"a":1},{"a":2}]`, []string{"a", "1", "a", "2"}, false, true},
		"value equal to a name":  {`{"a":"a","b":"a"}`, []string{"a", "a", "b", "a"}, false, true},
		"not JSON":               {`{"a":`, []string{"a"}, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			texts, repeated, ok := walkJSON([]byte(tc.document))
			assert.Equal(t, tc.texts, texts)
			assert.Equal(t, tc.repeated, repeated)
			assert.Equal(t, tc.ok, ok)
		})
	}
}
