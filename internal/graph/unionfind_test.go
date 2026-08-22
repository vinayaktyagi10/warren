package graph

import (
	"sort"
	"testing"
)

func TestUnionFindStartsDisjoint(t *testing.T) {
	u := NewUnionFind(5)
	for i := int32(0); i < 5; i++ {
		if got := u.Find(i); got != i {
			t.Errorf("Find(%d) = %d, want %d", i, got, i)
		}
	}
	if got := len(u.Components()); got != 0 {
		t.Errorf("singletons should not form components, got %d", got)
	}
}

func TestUnionReportsFirstMergeOnly(t *testing.T) {
	u := NewUnionFind(3)
	if !u.Union(0, 1) {
		t.Error("first union of distinct sets should report a merge")
	}
	if u.Union(0, 1) {
		t.Error("repeat union of the same set should report no merge")
	}
	if u.Union(1, 0) {
		t.Error("union is symmetric; reversed repeat should report no merge")
	}
}

// Transitivity is the property ring detection depends on: linking A-B and B-C
// must place A and C in one ring even though nothing directly links them.
func TestUnionIsTransitive(t *testing.T) {
	u := NewUnionFind(6)
	u.Union(0, 1)
	u.Union(1, 2)

	if u.Find(0) != u.Find(2) {
		t.Error("A-B and B-C should place A and C in one set")
	}
	if u.Find(0) == u.Find(3) {
		t.Error("unlinked elements should stay separate")
	}
}

func TestComponentsGroupsMembersAndDropsSingletons(t *testing.T) {
	u := NewUnionFind(7)
	u.Union(0, 1)
	u.Union(1, 2) // {0,1,2}
	u.Union(4, 5) // {4,5}
	// 3 and 6 stay singletons.

	comps := u.Components()
	if len(comps) != 2 {
		t.Fatalf("got %d components, want 2", len(comps))
	}

	var sizes []int
	members := map[int32]bool{}
	for _, m := range comps {
		sizes = append(sizes, len(m))
		for _, id := range m {
			members[id] = true
		}
	}
	sort.Ints(sizes)
	if want := []int{2, 3}; sizes[0] != want[0] || sizes[1] != want[1] {
		t.Errorf("component sizes = %v, want %v", sizes, want)
	}
	for _, singleton := range []int32{3, 6} {
		if members[singleton] {
			t.Errorf("singleton %d should be excluded", singleton)
		}
	}
}

// Chaining every element in sequence is the worst case for tree depth; it must
// still resolve to a single set rather than degrading into a linked list.
func TestUnionFindHandlesLongChain(t *testing.T) {
	const n = 100_000
	u := NewUnionFind(n)
	for i := int32(1); i < n; i++ {
		u.Union(i-1, i)
	}

	root := u.Find(0)
	for i := int32(0); i < n; i++ {
		if u.Find(i) != root {
			t.Fatalf("element %d not merged into the chain", i)
		}
	}
	if got := len(u.Components()); got != 1 {
		t.Errorf("got %d components, want 1", got)
	}
}
