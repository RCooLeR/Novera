// Package regexutil analyzes regular expressions before streaming work begins.
//
// Novera evaluates regexes in bounded windows. This package centralizes compile
// checks and warnings so callers can explain limitations such as anchors or
// very long matches before a large operation starts.
package regexutil
