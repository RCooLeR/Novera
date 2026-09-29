// Package csv provides CSV and delimited-text tools.
//
// It includes delimiter inspection, schema inference, previews, projection, and
// SQL conversion helpers. Each descriptor operation states its own input and
// memory model. Exact row deduplication uses explicit cardinality and retained
// memory budgets and refuses before either bound is exceeded.
package csv
