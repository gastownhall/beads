package schema

import (
	"reflect"
	"testing"
)

func TestContentHashSkew(t *testing.T) {
	cases := []struct {
		name          string
		local, remote map[int]string
		want          []int
	}{
		{
			name:   "identical -> no skew",
			local:  map[int]string{1: "a", 2: "b", 3: "c"},
			remote: map[int]string{1: "a", 2: "b", 3: "c"},
			want:   nil,
		},
		{
			name:   "one version diverges",
			local:  map[int]string{1: "a", 2: "b", 3: "c"},
			remote: map[int]string{1: "a", 2: "X", 3: "c"},
			want:   []int{2},
		},
		{
			name:   "multiple diverge, sorted",
			local:  map[int]string{1: "a", 2: "b", 3: "c"},
			remote: map[int]string{3: "Z", 2: "Y", 1: "a"},
			want:   []int{2, 3},
		},
		{
			name:   "version only on one side is ignored",
			local:  map[int]string{1: "a", 2: "b"},
			remote: map[int]string{1: "a"},
			want:   nil,
		},
		{
			name:   "empty/unknown hash on either side is ignored",
			local:  map[int]string{1: "a", 2: ""},
			remote: map[int]string{1: "", 2: "b"},
			want:   nil,
		},
		{
			name:   "empty maps",
			local:  map[int]string{},
			remote: map[int]string{},
			want:   nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ContentHashSkew(c.local, c.remote)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ContentHashSkew = %v, want %v", got, c.want)
			}
		})
	}
}
