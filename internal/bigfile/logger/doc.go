// Package logger writes structured diagnostic events.
//
// Long-running operations can fail, be cancelled, or detect safety conditions.
// This package records those events as JSON lines so the Activity UI and tests
// can inspect recent behavior without parsing free-form log text.
package logger
