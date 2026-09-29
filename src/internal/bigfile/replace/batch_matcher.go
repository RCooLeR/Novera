package replace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"

	"novera/internal/bigfile/asciifold"
)

type compiledBatchPattern struct {
	rule         compiledBatchRule
	length       int
	multiplicity int
}

type batchAhoNode struct {
	edgeStart  int
	edgeCount  int
	fail       int
	outputLink int
	terminals  []int
}

type batchAhoEdge struct {
	value byte
	next  int
}

type rawBatchAhoEdge struct {
	from  int
	value byte
	next  int
}

type compiledBatchSet struct {
	patterns        []compiledBatchPattern
	nodes           []batchAhoNode
	edges           []batchAhoEdge
	maxPattern      int
	caseInsensitive bool
}

type batchPatternBuild struct {
	needle  []byte
	pattern compiledBatchPattern
}

func compileBatchRules(rules []BatchRule, caseInsensitive bool) (*compiledBatchSet, int, error) {
	maxPattern, err := validateBatchRules(rules)
	if err != nil {
		return nil, 0, err
	}

	groups := make([]batchPatternBuild, 0, len(rules))
	groupByNeedle := make(map[string]int, len(rules))
	for i, original := range rules {
		rule := original
		if rule.Name == "" {
			rule.Name = fmt.Sprintf("Rule %d", i+1)
		}
		if rule.Priority == 0 && i > 0 {
			rule.Priority = i
		}
		rule.Find = bytes.Clone(original.Find)
		rule.Replace = bytes.Clone(original.Replace)
		compiledRule := compiledBatchRule{BatchRule: rule, order: i}

		needle := rule.Find
		if caseInsensitive {
			needle = asciifold.Fold(rule.Find)
		}
		key := string(needle)
		if groupIndex, found := groupByNeedle[key]; found {
			group := &groups[groupIndex].pattern
			group.multiplicity++
			candidate := batchCandidate{start: 0, end: len(rule.Find), rule: compiledRule}
			current := batchCandidate{start: 0, end: group.length, rule: group.rule}
			if betterBatchCandidate(candidate, current) {
				group.rule = compiledRule
			}
			continue
		}
		groupByNeedle[key] = len(groups)
		groups = append(groups, batchPatternBuild{
			needle: needle,
			pattern: compiledBatchPattern{
				rule:         compiledRule,
				length:       len(rule.Find),
				multiplicity: 1,
			},
		})
	}

	nodes := []batchAhoNode{{outputLink: -1}}
	rawEdges := make([]rawBatchAhoEdge, 0, min(MaxBatchPatternAggregateBytes, 64*1024))
	transitions := make(map[uint64]int, min(MaxBatchPatternAggregateBytes, 64*1024))
	transitionKey := func(state int, value byte) uint64 {
		return uint64(state)<<8 | uint64(value)
	}
	for patternIndex, group := range groups {
		state := 0
		for _, value := range group.needle {
			key := transitionKey(state, value)
			next, found := transitions[key]
			if !found {
				next = len(nodes)
				nodes = append(nodes, batchAhoNode{outputLink: -1})
				transitions[key] = next
				rawEdges = append(rawEdges, rawBatchAhoEdge{from: state, value: value, next: next})
			}
			state = next
		}
		nodes[state].terminals = append(nodes[state].terminals, patternIndex)
	}

	sort.Slice(rawEdges, func(i, j int) bool {
		if rawEdges[i].from != rawEdges[j].from {
			return rawEdges[i].from < rawEdges[j].from
		}
		return rawEdges[i].value < rawEdges[j].value
	})
	edges := make([]batchAhoEdge, len(rawEdges))
	for i, edge := range rawEdges {
		edges[i] = batchAhoEdge{value: edge.value, next: edge.next}
		if nodes[edge.from].edgeCount == 0 {
			nodes[edge.from].edgeStart = i
		}
		nodes[edge.from].edgeCount++
	}

	queue := make([]int, 0, len(nodes))
	for _, edge := range rawEdges {
		if edge.from != 0 {
			break
		}
		nodes[edge.next].fail = 0
		nodes[edge.next].outputLink = -1
		queue = append(queue, edge.next)
	}
	for head := 0; head < len(queue); head++ {
		state := queue[head]
		node := nodes[state]
		for i := node.edgeStart; i < node.edgeStart+node.edgeCount; i++ {
			edge := edges[i]
			fallback := nodes[state].fail
			for {
				if target, found := transitions[transitionKey(fallback, edge.value)]; found && target != edge.next {
					nodes[edge.next].fail = target
					break
				}
				if fallback == 0 {
					nodes[edge.next].fail = 0
					break
				}
				fallback = nodes[fallback].fail
			}
			failure := nodes[edge.next].fail
			if len(nodes[failure].terminals) > 0 {
				nodes[edge.next].outputLink = failure
			} else {
				nodes[edge.next].outputLink = nodes[failure].outputLink
			}
			queue = append(queue, edge.next)
		}
	}

	patterns := make([]compiledBatchPattern, len(groups))
	for i := range groups {
		patterns[i] = groups[i].pattern
	}
	return &compiledBatchSet{
		patterns:        patterns,
		nodes:           nodes,
		edges:           edges,
		maxPattern:      maxPattern,
		caseInsensitive: caseInsensitive,
	}, maxPattern, nil
}

func (set *compiledBatchSet) transition(state int, value byte) (int, bool) {
	node := set.nodes[state]
	index := sort.Search(node.edgeCount, func(i int) bool {
		return set.edges[node.edgeStart+i].value >= value
	})
	if index >= node.edgeCount {
		return 0, false
	}
	edge := set.edges[node.edgeStart+index]
	if edge.value != value {
		return 0, false
	}
	return edge.next, true
}

type batchStartBucket struct {
	start       int
	count       int
	bestPattern int
}

type batchCandidateIterator struct {
	ctx       context.Context
	window    []byte
	windowAt  int64
	sourceLen int64
	wholeWord bool
	set       *compiledBatchSet

	scan     int
	state    int
	cursor   int
	searchAt int
	buckets  []batchStartBucket

	conflictActive bool
	conflictEnd    int
	conflicts      int
}

func newBatchCandidateIterator(ctx context.Context, window []byte, windowAt int64, sourceLen int64, wholeWord bool, set *compiledBatchSet) *batchCandidateIterator {
	buckets := make([]batchStartBucket, set.maxPattern+1)
	for i := range buckets {
		buckets[i].start = -1
		buckets[i].bestPattern = -1
	}
	return &batchCandidateIterator{
		ctx:       ctx,
		window:    window,
		windowAt:  windowAt,
		sourceLen: sourceLen,
		wholeWord: wholeWord,
		set:       set,
		buckets:   buckets,
	}
}

func (it *batchCandidateIterator) finalizedThrough() int {
	if it.scan == len(it.window) {
		return len(it.window) - 1
	}
	return it.scan - it.set.maxPattern
}

func (it *batchCandidateIterator) addPattern(patternIndex int) error {
	pattern := it.set.patterns[patternIndex]
	start := it.scan - pattern.length
	if start < it.cursor {
		return nil
	}
	if !replaceWordBoundaryOK(it.window, start, pattern.length, it.windowAt, it.sourceLen, it.wholeWord) {
		return nil
	}
	if it.conflictActive && start < it.conflictEnd {
		it.conflicts += pattern.multiplicity
		return nil
	}

	slot := &it.buckets[start%len(it.buckets)]
	if slot.start != start {
		if slot.start >= it.cursor && slot.count != 0 {
			return errors.New("internal batch matcher bucket overflow")
		}
		slot.start = start
		slot.count = 0
		slot.bestPattern = -1
	}
	candidate := batchCandidate{start: start, end: start + pattern.length, rule: pattern.rule}
	if slot.bestPattern < 0 {
		slot.bestPattern = patternIndex
	} else {
		currentPattern := it.set.patterns[slot.bestPattern]
		current := batchCandidate{start: start, end: start + currentPattern.length, rule: currentPattern.rule}
		if betterBatchCandidate(candidate, current) {
			slot.bestPattern = patternIndex
		}
	}
	slot.count += pattern.multiplicity
	return nil
}

func (it *batchCandidateIterator) scanByte() error {
	if it.scan&4095 == 0 {
		if err := it.ctx.Err(); err != nil {
			return err
		}
	}
	value := it.window[it.scan]
	if it.set.caseInsensitive {
		value = asciifold.Lower(value)
	}
	for {
		if next, found := it.set.transition(it.state, value); found {
			it.state = next
			break
		}
		if it.state == 0 {
			break
		}
		it.state = it.set.nodes[it.state].fail
	}
	it.scan++

	outputState := it.state
	visited := 0
	for outputState >= 0 {
		for _, patternIndex := range it.set.nodes[outputState].terminals {
			if err := it.addPattern(patternIndex); err != nil {
				return err
			}
			visited++
			if visited&255 == 0 {
				if err := it.ctx.Err(); err != nil {
					return err
				}
			}
		}
		outputState = it.set.nodes[outputState].outputLink
	}
	return nil
}

func (it *batchCandidateIterator) next() (batchCandidate, int, bool, error) {
	if err := it.ctx.Err(); err != nil {
		return batchCandidate{}, 0, false, err
	}
	for {
		finalized := it.finalizedThrough()
		for it.searchAt <= finalized {
			start := it.searchAt
			it.searchAt++
			if start < it.cursor {
				continue
			}
			slot := &it.buckets[start%len(it.buckets)]
			if slot.start != start || slot.count == 0 {
				continue
			}
			pattern := it.set.patterns[slot.bestPattern]
			winner := batchCandidate{start: start, end: start + pattern.length, rule: pattern.rule}

			it.conflicts = -1
			for conflictStart := winner.start; conflictStart < winner.end; conflictStart++ {
				conflictSlot := &it.buckets[conflictStart%len(it.buckets)]
				if conflictSlot.start == conflictStart {
					it.conflicts += conflictSlot.count
					conflictSlot.start = -1
					conflictSlot.count = 0
					conflictSlot.bestPattern = -1
				}
			}
			it.conflictActive = true
			it.conflictEnd = winner.end
			for it.finalizedThrough() < winner.end-1 && it.scan < len(it.window) {
				if err := it.scanByte(); err != nil {
					return batchCandidate{}, 0, false, err
				}
			}
			it.conflictActive = false
			if it.conflicts < 0 {
				return batchCandidate{}, 0, false, errors.New("internal batch matcher lost selected candidate")
			}
			it.cursor = winner.end
			if it.searchAt < it.cursor {
				it.searchAt = it.cursor
			}
			return winner, it.conflicts, true, nil
		}
		if it.scan == len(it.window) {
			return batchCandidate{}, 0, false, nil
		}
		if err := it.scanByte(); err != nil {
			return batchCandidate{}, 0, false, err
		}
	}
}

type batchCandidateEmitter func(batchCandidate, int) error

func arbitrateBatchPrefix(ctx context.Context, window []byte, processLimit int, sourceSize int64, windowStart int64, set *compiledBatchSet, wholeWord bool, maxMatches int, emit batchCandidateEmitter) (int, int, int, error) {
	iterator := newBatchCandidateIterator(ctx, window, windowStart, sourceSize, wholeWord, set)
	consumed := 0
	matches := 0
	conflicts := 0
	for {
		candidate, candidateConflicts, found, err := iterator.next()
		if err != nil {
			return 0, matches, conflicts, err
		}
		if !found {
			break
		}
		if candidate.start >= processLimit {
			safeStart := candidate.start - utf8.UTFMax
			if safeStart > processLimit {
				safeStart = processLimit
			}
			if safeStart < 0 {
				safeStart = 0
			}
			if safeStart < consumed {
				safeStart = consumed
			}
			return safeStart, matches, conflicts, nil
		}
		if candidate.end > processLimit {
			safeStart := candidate.start
			if wholeWord && safeStart > 0 {
				safeStart--
			}
			if safeStart < consumed {
				safeStart = consumed
			}
			return safeStart, matches, conflicts, nil
		}
		if err := emit(candidate, candidateConflicts); err != nil {
			return 0, matches, conflicts, err
		}
		matches++
		conflicts += candidateConflicts
		consumed = candidate.end
		if maxMatches > 0 && matches >= maxMatches {
			return consumed, matches, conflicts, nil
		}
	}
	if consumed < processLimit {
		consumed = processLimit
	}
	return consumed, matches, conflicts, nil
}
