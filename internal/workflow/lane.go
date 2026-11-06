package workflow

import "slices"

// Lane is what lies past one step in the branch holding it: each sequence and repeat between the
// step and its nearest enclosing parallel. The zero Lane holds nothing, so nothing passes it.
type Lane struct {
	forks []laneFork
}

// laneFork is one container of a lane: the step sits under prefix at segment own, and before holds a
// sequence's earlier children, nil for a repeat, whose every other pass lies past the step.
type laneFork struct {
	before map[string]bool
	own    string
	prefix []string
}

// LaneOf is the lane of the step at PathKey key in s, the zero Lane when s holds no step there.
func LaneOf(s *State, key string) Lane {
	if s == nil || s.Root == nil {
		return Lane{}
	}
	var lane Lane
	var walk func(n *node, chain []visit, path []string) bool
	walk = func(n *node, chain []visit, path []string) bool {
		if n.Type == nodeTypeStep && PathKey(path) == key {
			lane = laneAlong(chain, path)
			return true
		}
		chain = append(chain, visit{n: n, path: path})
		for i := range n.Children {
			c := &n.Children[i]
			if walk(c, chain, append(slices.Clip(path), pathSegment(c, n))) {
				return true
			}
		}
		return false
	}
	walk(s.Root, nil, []string{pathSegment(s.Root, nil)})
	return lane
}

// laneAlong reads the forks off a step's ancestors, nearest first, stopping at a parallel: past
// its join every sibling branch has stopped.
func laneAlong(chain []visit, step []string) Lane {
	var out Lane
	for _, a := range slices.Backward(chain) {
		own := step[len(a.path)]
		switch a.n.Type {
		case "parallel":
			return out
		case "sequence":
			f := laneFork{prefix: a.path, own: own, before: map[string]bool{}}
			for j := range a.n.Children {
				seg := pathSegment(&a.n.Children[j], a.n)
				if seg == own {
					break
				}
				f.before[seg] = true
			}
			out.forks = append(out.forks, f)
		case "repeat":
			out.forks = append(out.forks, laneFork{prefix: a.path, own: own})
		}
	}
	return out
}

// Passed reports whether the node at path lies past the lane's step: a later child of a sequence
// holding it, or another pass of a repeat holding it.
func (l Lane) Passed(path []string) bool {
	for _, f := range l.forks {
		if len(path) <= len(f.prefix) || !slices.Equal(path[:len(f.prefix)], f.prefix) {
			continue
		}
		if seg := path[len(f.prefix)]; seg != f.own && !f.before[seg] {
			return true
		}
	}
	return false
}
