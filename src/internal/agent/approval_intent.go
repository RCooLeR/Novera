package agent

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"novera/internal/workspace"
)

const (
	approvalIntentVersion = 1
	maxApprovalArgsBytes  = 1 << 20
	// Artifact registration accepts up to 4,096 lineage sources in addition to
	// the artifact itself, so the approval envelope permits exactly that fanout.
	maxApprovalResources        = 4097
	maxApprovalContentHashBytes = 64 << 20
	approvalDigestHexBytes      = sha256.Size * 2
)

type approvalIntentEnvelope struct {
	Version             int                                  `json:"version"`
	RunID               string                               `json:"runId"`
	CallID              string                               `json:"callId"`
	Tool                string                               `json:"tool"`
	Args                json.RawMessage                      `json:"args"`
	WorkspaceRoot       string                               `json:"workspaceRoot"`
	WorkspaceGeneration uint64                               `json:"workspaceGeneration"`
	Resources           []workspace.ApprovalResourceIdentity `json:"resources,omitempty"`
	ExpiresAt           string                               `json:"expiresAt"`
}

type approvalIntentState struct {
	canonical           string
	digest              string
	expiresAt           time.Time
	workspaceRoot       string
	workspaceGeneration uint64
	resources           []workspace.ApprovalResourceIdentity
}

func (s *Service) buildApprovalIntent(runID, callID, tool, rawArgs string, expiresAt time.Time) (approvalIntentState, error) {
	var state approvalIntentState
	if len(rawArgs) > maxApprovalArgsBytes {
		return state, fmt.Errorf("approval intent arguments exceed the %d-byte limit", maxApprovalArgsBytes)
	}

	decoder := json.NewDecoder(strings.NewReader(rawArgs))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return state, fmt.Errorf("invalid approval intent arguments: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return state, fmt.Errorf("invalid approval intent arguments: %w", err)
	}
	args, ok := decoded.(map[string]any)
	if !ok {
		return state, errors.New("approval intent arguments must be a JSON object")
	}
	canonicalArgs, err := json.Marshal(args)
	if err != nil {
		return state, fmt.Errorf("canonicalize approval arguments: %w", err)
	}

	root, generation := workspace.ApprovalWorkspaceIdentity(s.ws)
	resources, err := s.captureApprovalResources(tool, args)
	if err != nil {
		return state, err
	}
	if currentRoot, currentGeneration := workspace.ApprovalWorkspaceIdentity(s.ws); currentRoot != root || currentGeneration != generation {
		return state, errors.New("workspace changed while the approval intent was being created")
	}
	expiresAt = expiresAt.UTC()
	envelope := approvalIntentEnvelope{
		Version:             approvalIntentVersion,
		RunID:               runID,
		CallID:              callID,
		Tool:                tool,
		Args:                canonicalArgs,
		WorkspaceRoot:       root,
		WorkspaceGeneration: generation,
		Resources:           resources,
		ExpiresAt:           expiresAt.Format(time.RFC3339Nano),
	}
	// MarshalIndent is deterministic for this struct/map shape and makes the
	// exact bytes covered by the digest directly readable in the renderer.
	canonical, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return state, fmt.Errorf("canonicalize approval intent: %w", err)
	}
	if len(canonical) > maxApprovalArgsBytes+(256<<10) {
		return state, errors.New("canonical approval intent is too large to review safely")
	}
	sum := sha256.Sum256(canonical)
	return approvalIntentState{
		canonical:           string(canonical),
		digest:              hex.EncodeToString(sum[:]),
		expiresAt:           expiresAt,
		workspaceRoot:       root,
		workspaceGeneration: generation,
		resources:           resources,
	}, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values are not allowed")
}

func (s *Service) captureApprovalResources(tool string, args map[string]any) ([]workspace.ApprovalResourceIdentity, error) {
	paths := approvalResourcePaths(tool, args)
	if len(paths) == 0 {
		return nil, nil
	}
	if len(paths) > maxApprovalResources {
		return nil, fmt.Errorf("approval intent references %d resources; limit is %d", len(paths), maxApprovalResources)
	}
	resources := make([]workspace.ApprovalResourceIdentity, 0, len(paths))
	var hashedBytes int64
	for _, rel := range paths {
		identity, err := workspace.CaptureApprovalResource(s.ws, rel)
		if err != nil {
			return nil, fmt.Errorf("capture approval resource %q: %w", rel, err)
		}
		if identity.HashedBytes > maxApprovalContentHashBytes-hashedBytes {
			return nil, fmt.Errorf("approval intent content hashing exceeds the %d-byte aggregate limit", maxApprovalContentHashBytes)
		}
		hashedBytes += identity.HashedBytes
		resources = append(resources, identity)
	}
	return resources, nil
}

func approvalResourcePaths(tool string, args map[string]any) []string {
	keys := []string(nil)
	switch tool {
	case "csv_to_sql", "extract_dump_table", "dump_table_to_csv", "clean_sql_dump", "csv_select_columns", "csv_add_column":
		keys = []string{"path", "outPath"}
	case "split_dump":
		keys = []string{"path", "outDir"}
	case "write_file", "apply_edit", "append_file", "apply_patch", "delete_file":
		keys = []string{"path"}
	case "copy_file", "move_file":
		keys = []string{"from", "to"}
	case "create_artifact":
		keys = []string{"path"}
	}

	seen := make(map[string]struct{}, len(keys))
	paths := make([]string, 0, len(keys)+4)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		normalized := strings.ReplaceAll(value, "\\", "/")
		if _, exists := seen[normalized]; exists {
			return
		}
		seen[normalized] = struct{}{}
		paths = append(paths, value)
	}
	for _, key := range keys {
		if value, ok := args[key].(string); ok {
			add(value)
		}
	}
	if tool == "create_artifact" {
		if values, ok := args["sources"].([]any); ok {
			for _, value := range values {
				if path, ok := value.(string); ok {
					add(path)
				}
			}
		}
	}
	sort.Strings(paths)
	return paths
}

func (s *Service) validateApprovalIntent(state approvalIntentState) error {
	if time.Now().After(state.expiresAt) {
		return errors.New("approval intent expired before execution")
	}
	root, generation := workspace.ApprovalWorkspaceIdentity(s.ws)
	if root != state.workspaceRoot || generation != state.workspaceGeneration {
		return errors.New("workspace changed after the operation was reviewed")
	}
	for _, expected := range state.resources {
		actual, err := workspace.CaptureApprovalResource(s.ws, expected.Path)
		if err != nil {
			return fmt.Errorf("revalidate approval resource %q: %w", expected.Path, err)
		}
		if actual != expected {
			return fmt.Errorf("workspace resource %q changed after the operation was reviewed", expected.Path)
		}
	}
	return nil
}

func validApprovalDigest(actual, expected string) bool {
	if len(actual) != approvalDigestHexBytes || len(expected) != approvalDigestHexBytes {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

// approvalIntentDigest is intentionally small and testable: it proves that the
// exact canonical bytes displayed by the renderer are what the backend bound
// to the one-shot approval record.
func approvalIntentDigest(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}
