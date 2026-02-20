package module

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/hashicorp/go-multierror"

	"github.com/selesy/lichen/internal/model"
)

func Fetch(ctx context.Context, refs []model.ModuleReference) ([]model.Module, error) {
	if len(refs) == 0 {
		return []model.Module{}, nil
	}

	goBin, err := exec.LookPath("go")
	if err != nil {
		return nil, err
	}

	// normalize the path
	goBin, err = filepath.Abs(goBin)
	if err != nil {
		return nil, err
	}
	// resolve symlinks to prevent directory traversal attacks
	goBin, err = filepath.EvalSymlinks(goBin)
	if err != nil {
		return nil, err
	}

	// Validation: Ensure we actually found a 'go' binary
	if filepath.Base(goBin) != "go" && filepath.Base(goBin) != "go.exe" {
		return nil, errors.New("unexpected binary resolved")
	}

	tempDir, err := os.MkdirTemp("", "lichen")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	defer func() {
		if err := os.Remove(tempDir); err != nil {
			slog.Error("failed to remove temporary folder/files", slog.String("reason", err.Error()))
		}
	}()

	args := []string{"mod", "download", "-json"}
	for _, ref := range refs {
		if !ref.IsLocal() {
			args = append(args, ref.String())
		}
	}

	//nolint:gosec
	// the goBin variable was sanitized above
	cmd := exec.CommandContext(ctx, goBin, args...)
	cmd.Dir = tempDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch: %w (output: %s)", err, string(out))
	}

	// parse JSON output from `go mod download`
	modules := make([]model.Module, 0)
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var m model.Module
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		modules = append(modules, m)
	}

	// add local modules, as these won't be included in the set returned by `go mod download`
	for _, ref := range refs {
		if ref.IsLocal() {
			modules = append(modules, model.Module{
				ModuleReference: ref,
			})
		}
	}

	// sanity check: all modules should have been covered in the output from `go mod download`
	if err := verifyFetched(modules, refs); err != nil {
		return nil, fmt.Errorf("failed to fetch all modules: %w", err)
	}

	return modules, nil
}

func verifyFetched(fetched []model.Module, requested []model.ModuleReference) (err error) {
	fetchedRefs := make(map[model.ModuleReference]struct{}, len(fetched))
	for _, module := range fetched {
		fetchedRefs[module.ModuleReference] = struct{}{}
	}
	for _, ref := range requested {
		if _, found := fetchedRefs[ref]; !found {
			err = multierror.Append(err, fmt.Errorf("module %s could not be resolved", ref))
		}
	}
	return
}
