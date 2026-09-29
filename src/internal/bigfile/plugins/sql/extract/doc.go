// Package extract plans and writes SQL table extraction outputs.
//
// It uses byte ranges discovered by SQL analysis to copy selected table ranges
// into new files. The source dump remains unchanged.
package extract
