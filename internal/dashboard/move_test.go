package dashboard

import (
	"reflect"
	"testing"
)

func TestMoveBefore(t *testing.T) {
	ids := []int{1, 2, 3, 4}
	cases := []struct {
		name       string
		id, before int
		want       []int
		ok         bool
	}{
		{name: "up", id: 3, before: 1, want: []int{3, 1, 2, 4}, ok: true},
		{name: "down", id: 1, before: 4, want: []int{2, 3, 1, 4}, ok: true},
		{name: "to end", id: 2, before: 0, want: []int{1, 3, 4, 2}, ok: true},
		{name: "missing target", id: 2, before: 99, ok: false},
		{name: "onto itself", id: 2, before: 2, want: []int{1, 2, 3, 4}, ok: true},
		{name: "missing site", id: 9, before: 1, ok: false},
	}

	for _, tc := range cases {
		got, ok := moveBefore(ids, tc.id, tc.before)
		if ok != tc.ok || (ok && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("%s: got %v %v, want %v %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
	if !reflect.DeepEqual(ids, []int{1, 2, 3, 4}) {
		t.Errorf("input was modified: %v", ids)
	}
}
