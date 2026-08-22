package graph

// UnionFind is a disjoint-set forest over a fixed number of elements.
//
// Ring detection is transitive: if client A shares a device with B, and B shares
// a card with C, then A, B and C are one ring. Union-find gives that closure in
// effectively linear time without ever materialising the edge list, which
// matters because the alternative — repeated SQL self-joins to chase links —
// grows badly and is where naive entity resolution usually falls over.
type UnionFind struct {
	parent []int32
	rank   []uint8
}

// NewUnionFind creates n singleton sets.
func NewUnionFind(n int) *UnionFind {
	parent := make([]int32, n)
	for i := range parent {
		parent[i] = int32(i)
	}
	return &UnionFind{parent: parent, rank: make([]uint8, n)}
}

// Len reports how many elements the structure covers.
func (u *UnionFind) Len() int { return len(u.parent) }

// Find returns the representative of x's set, flattening the path it walks so
// repeated lookups stay near constant time.
func (u *UnionFind) Find(x int32) int32 {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]] // path halving
		x = u.parent[x]
	}
	return x
}

// Union merges the sets containing a and b, reporting whether they were
// distinct beforehand. Attaching the shallower tree to the deeper one keeps
// depth logarithmic even before path compression.
func (u *UnionFind) Union(a, b int32) bool {
	ra, rb := u.Find(a), u.Find(b)
	if ra == rb {
		return false
	}
	if u.rank[ra] < u.rank[rb] {
		ra, rb = rb, ra
	}
	u.parent[rb] = ra
	if u.rank[ra] == u.rank[rb] {
		u.rank[ra]++
	}
	return true
}

// Components groups the elements by set, keyed by representative. Only sets
// with more than one member are returned: a client linked to nothing is not a
// ring, and carrying hundreds of thousands of singletons would swamp callers.
func (u *UnionFind) Components() map[int32][]int32 {
	sizes := make(map[int32]int)
	for i := range u.parent {
		sizes[u.Find(int32(i))]++
	}
	out := make(map[int32][]int32)
	for i := range u.parent {
		root := u.Find(int32(i))
		if sizes[root] > 1 {
			out[root] = append(out[root], int32(i))
		}
	}
	return out
}
