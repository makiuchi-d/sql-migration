package dbstate

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

func compareRef(a, b Ref) int {
	if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
		return c
	}
	return cmp.Compare(a.Name, b.Name)
}

// SortObjects sorts the objects in (Kind, Name) order as far as the
// dependencies allow (a stable topological sort), so that the same state
// always gives the same order.
//
// A dependency to an object not in the list is ignored.
// A circular dependency is an error.
func SortObjects(objs []*Object) ([]*Object, error) {
	rest := slices.Clone(objs)
	slices.SortFunc(rest, func(a, b *Object) int { return compareRef(a.Ref(), b.Ref()) })

	exists := make(map[Ref]bool, len(objs))
	for _, o := range objs {
		exists[o.Ref()] = true
	}
	done := make(map[Ref]bool, len(objs))
	ready := func(o *Object) bool {
		for _, d := range o.Deps {
			if d != o.Ref() && exists[d] && !done[d] {
				return false
			}
		}
		return true
	}

	sorted := make([]*Object, 0, len(objs))
	for len(rest) > 0 {
		i := slices.IndexFunc(rest, ready)
		if i < 0 {
			names := make([]string, len(rest))
			for j, o := range rest {
				names[j] = fmt.Sprintf("%s %q", o.Kind, o.Name)
			}
			return nil, fmt.Errorf("circular dependency: %s", strings.Join(names, ", "))
		}
		o := rest[i]
		sorted = append(sorted, o)
		done[o.Ref()] = true
		rest = slices.Delete(rest, i, i+1)
	}
	return sorted, nil
}
